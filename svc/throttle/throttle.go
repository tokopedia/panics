package throttle

import (
	"time"
)

//Setup do initial setup
func Setup(max, retryAfter int) {
	maxSendMessage = defaultMaxSendMessage
	if max != 0 {
		maxSendMessage = max
	}

	go func() {
		waitTime := defaultRetrySendMessage
		if retryAfter != 0 {
			waitTime = retryAfter
		}
		for {
			time.Sleep(time.Duration(waitTime) * time.Second)
			sendMessageCounter = 0
		}
	}()
}
