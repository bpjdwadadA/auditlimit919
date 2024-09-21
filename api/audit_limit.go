package api

import (
	"auditlimit/config"
	"context"
	"strings"
	"sync"
	"time"
    "strconv" // 添加这一行
	"github.com/go-redis/redis/v8"
	"github.com/gogf/gf/v2/encoding/gjson"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/util/gconv"
)

var (
	redisClient     *redis.Client
	rateLimitMutex  sync.Mutex
)

func init() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:     "redis:6379", // Redis 服务器地址
		Password: "",                          // 密码
		DB:       2,                           // 数据库编号
		PoolSize: 300,                        // 连接池大小
	})

	// 检查 Redis 连接
	ctx := context.Background()
	pong, err := redisClient.Ping(ctx).Result()
	if err != nil {
		g.Log().Fatal(ctx, "Failed to connect to Redis:", err)
	}
	g.Log().Info(ctx, "Redis connection established:", pong)
}

func AuditLimit(r *ghttp.Request) {
	ctx := r.Context()
	
	// 获取Bearer Token 用来判断用户身份
	token := getTokenFromRequest(r)
	if token == "" {
		r.Response.Status = 401
		r.Response.WriteJson(g.Map{"detail": "Unauthorized request, missing token."})
		return
	}
	g.Log().Debug(ctx, "token", token)

	// 获取其他头部信息
	gfsessionid := r.Cookie.Get("gfsessionid").String()
	referer := r.Header.Get("referer")
	g.Log().Debug(ctx, "gfsessionid", gfsessionid, "referer", referer)

	// 获取请求内容
	reqJson, err := r.GetJson()
	if err != nil {
		g.Log().Error(ctx, "GetJson", err)
		r.Response.Status = 400
		r.Response.WriteJson(g.Map{"detail": err.Error()})
		return
	}

	// 安全获取 action
	action := reqJson.Get("action").String()
	g.Log().Debug(ctx, "action", action)

	// 安全获取 model
	model := reqJson.Get("model").String()
	g.Log().Debug(ctx, "model", model)

	// 安全获取 prompt
	prompt := reqJson.Get("messages.0.content.parts.0").String()
	if prompt == "" {
		g.Log().Error(ctx, "Missing messages content.")
		r.Response.Status = 400
		r.Response.WriteJson(g.Map{"detail": "请求内容缺少必要的字段"})
		return
	}
	g.Log().Debug(ctx, "prompt", prompt)

	// 判断提问内容是否包含禁止词
	if containsAny(ctx, prompt, config.ForbiddenWords) {
		r.Response.Status = 400
		r.Response.WriteJson(g.Map{"detail": "此段内容中有群主设置的违禁词，请修改内容重新提问"})
		return
	}

	// OPENAI Moderation 检测
	if config.OAIKEY != "" {
		if !moderationCheck(ctx, prompt) {
			r.Response.Status = 400
			r.Response.WriteJson(g.Map{"detail": "内容审核未通过，请遵循内容审核规则"})
			return
		}
	}

	// 检查用户是否为 plus 用户
	isPlusUser := checkPlusUser(ctx, token)

	// 如果不是 plus 用户，只能使用 gpt-4o-mini 模型
	if !isPlusUser && model != "gpt-4o-mini" {
		r.Response.Status = 400
		r.Response.WriteJson(g.Map{"detail": "这是高级会员的模型，请点击对话下面得小闪电图标或者是向下箭头得图标切换到 gpt-4o-mini 模型。"})
		return
	}

	// 根据模型进行频率限制检查
	if config.O1Models.Contains(model) {
		handleRateLimit(ctx, r, token+"|o1model", config.O1LIMIT, config.O1PER)
		return
	}

	if config.PlusModels.Contains(model) {
		handleRateLimit(ctx, r, token, config.LIMIT, config.PER)
		return
	}

	r.Response.Status = 200
}

// 从请求中提取 Bearer Token
func getTokenFromRequest(r *ghttp.Request) string {
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		return token[7:]
	}
	return ""
}

// 判断字符串是否包含数组中的任意一个元素
func containsAny(ctx g.Ctx, text string, array []string) bool {
	for _, item := range array {
		if strings.Contains(text, item) {
			g.Log().Debug(ctx, "containsAny", text, item)
			return true
		}
	}
	return false
}

// 调用 OpenAI Moderation API 进行内容审核
func moderationCheck(ctx context.Context, prompt string) bool {
	respVar := g.Client().SetHeaderMap(g.MapStrStr{
		"Authorization": "Bearer " + config.OAIKEY,
		"Content-Type":  "application/json",
	}).PostVar(ctx, config.MODERATION, g.Map{"input": prompt})

	if respVar == nil {
		g.Log().Error(ctx, "Moderation API request failed")
		return false
	}

	respJson := gjson.New(respVar)
	return !respJson.Get("results.0.flagged").Bool()
}

// 检查用户是否为 plus 用户
func checkPlusUser(ctx context.Context, token string) bool {
	// 从缓存中获取用户的 Plus 状态
	cacheKey := "plus_user:" + token
	isPlus, err := redisClient.Get(ctx, cacheKey).Result()
	if err == nil {
		return isPlus == "true"
	}

	// 如果从 Redis 获取缓存失败，记录日志
	if err != redis.Nil {
		g.Log().Error(ctx, "Failed to get plus user status from Redis:", err)
	}

	// 如果缓存中不存在，调用外部 API 进行检查
	resp, err := g.Client().SetHeaderMap(g.MapStrStr{
		"Content-Type": "application/json",
	}).Post(ctx, "https://gpt.bpjgpt.top/check_plus_end_time", g.Map{"token": token})
	if err != nil {
		g.Log().Error(ctx, "checkPlusUser error:", err)
		return false
	}
	defer resp.Close()

	respJson := gjson.New(resp.ReadAllString())
	isPlusValid := respJson.Get("is_plus_valid").Bool()

	// 将结果存入缓存，设置过期时间为 4 小时
	redisClient.Set(ctx, cacheKey, strconv.FormatBool(isPlusValid), 4*time.Hour).Err()

	return isPlusValid
}


// 处理频率限制逻辑
func handleRateLimit(ctx context.Context, r *ghttp.Request, token string, limit int, period time.Duration) {
	rateLimitMutex.Lock()
	defer rateLimitMutex.Unlock()

	limiter := GetVisitor(token, limit, period)
	remain := limiter.TokensAt(time.Now())
	g.Log().Debug(ctx, "remaining tokens", remain)

	if remain < 1 {
		waitTime := calculateWaitTime(period, limit, remain)
		r.Response.Status = 429
		r.Response.WriteJson(g.Map{
			"detail": "You have triggered the usage frequency limit, the current limit is " + gconv.String(limit) + " times/" + gconv.String(period) + ", please wait " + gconv.String(int(waitTime.Seconds())) + " seconds before trying again.",
		})
		return
	}
	limiter.Allow()
	r.Response.Status = 200
}

// 动态计算等待时间
func calculateWaitTime(period time.Duration, limit int, remain float64) time.Duration {
	createInterval := period / time.Duration(limit)
	wait := (1 - remain) * createInterval.Seconds()
	return time.Duration(wait) * time.Second
}
