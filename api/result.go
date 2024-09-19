package api

var (
	MsgPlus429 = `
	{
		"detail": {
		  "clears_in": 252,
		  "code": "model_cap_exceeded",
		  "message": "You have sent too many messages to the model. Please try again later.您向模型发送了过多的消息，已经超过了允许的请求频率限制."
		}
	  }
	`

	MsgMod400 = `
	{
		"detail": {
		  "code": "flagged_by_moderation",
		  "message": "This content may violate [OpenAI Usage Policies](https://openai.com/policies/usage-policies).此内容可能违反 OpenAI [OpenAI使用政策](https://openai.com/policies/usage-policies)"
		}
	}
	`
)
