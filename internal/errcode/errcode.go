package errcode

import "strings"

// A8 扫码绑定错误码语义化：让用户知道发生了什么、下一步做什么。
type Code string

const (
	OK        Code = "OK"          // 成功
	CONFIRMED Code = "CONFIRMED"   // 已确认绑定
	EXPIRED   Code = "QR_EXPIRED"  // 二维码已过期
	KICKED    Code = "KICKED"      // 被踢下线 / 登录态失效
	TIMEOUT   Code = "NET_TIMEOUT" // 网络超时 / 未及时拿到二维码
	QUEUED    Code = "QUEUED"      // 排队中
	BUSY      Code = "BUSY"        // 服务繁忙（内存/资源不足）
	UNKNOWN   Code = "UNKNOWN"
)

// Hint 给最终用户看的中文说明 + 下一步动作。
type Hint struct {
	Code     Code   `json:"code"`
	Message  string `json:"message"`
	NextStep string `json:"nextStep"`
}

var hints = map[Code]Hint{
	OK:        {OK, "成功", ""},
	CONFIRMED: {CONFIRMED, "绑定成功", "你的微信已连上 IM2A，直接发消息即可开始使用"},
	EXPIRED:   {EXPIRED, "二维码已过期", "点「重新生成二维码」再用微信扫一次"},
	KICKED:    {KICKED, "登录态已失效（被踢下线）", "请重新扫码绑定；如多次被踢，确认该微信未在别处登录同一会话"},
	TIMEOUT:   {TIMEOUT, "网络超时，没及时拿到二维码", "请检查网络后点「重新生成」重试"},
	QUEUED:    {QUEUED, "当前扫码人数较多，排队中", "无需重复点击，系统会自动放行，放好二维码即可"},
	BUSY:      {BUSY, "服务器繁忙（资源不足）", "请稍等 1-2 分钟后再试"},
	UNKNOWN:   {UNKNOWN, "出现未知问题", "请稍后重试，或联系管理员"},
}

func Of(c Code) Hint { return hints[c] }

// FromErr 从底层错误文本推断错误码。
func FromErr(err error) Hint {
	if err == nil {
		return hints[OK]
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "queued"):
		return hints[QUEUED]
	case strings.Contains(s, "内存不足"):
		return hints[BUSY]
	case strings.Contains(s, "未拿到二维码") || strings.Contains(s, "30s"):
		return hints[TIMEOUT]
	case strings.Contains(s, "已过期"):
		return hints[EXPIRED]
	case strings.Contains(s, "踢") || strings.Contains(s, "下线"):
		return hints[KICKED]
	default:
		h := hints[UNKNOWN]
		h.Message = s
		return h
	}
}
