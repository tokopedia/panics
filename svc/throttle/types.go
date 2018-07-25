package throttle

var (
	sendMessageCounter int
	maxSendMessage     int
	counter            int
)

type throttle struct{}

const (
	defaultRetrySendMessage = 600 // 10 minutes
	defaultMaxSendMessage   = 10
)
