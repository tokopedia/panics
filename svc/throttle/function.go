package throttle

//AllowedSend verifying send message
func AllowedSend() bool {
	if counter > maxSendMessage {
		return false
	}

	counter++
	return true
}
