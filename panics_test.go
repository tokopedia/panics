package panics

import (
  "testing"
)

// if the post to slack fails for some reason, it must log it
func TestPostToSlack(t *testing.T) {
  slackWebhookURL = "http://127.0.0.2";
  postToSlack("hello", "world")
}
