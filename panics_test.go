package panics

import (
	"bufio"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	SetOptions(&Options{
		Env:      "development",
		Filepath: "example",
	})
	os.Exit(m.Run())
}

// if the post to slack fails for some reason, it must log it
func TestPostToSlack(t *testing.T) {
	slackWebhookURL = "http://127.0.0.2"
	postToSlack("hello", "world")
}

func TestCaptureWithStackTrace(t *testing.T) {
	tests := []struct {
		name     string
		errStr   string
		messages []string
		want     []string // assertion per lines of output, starting from first line
	}{
		{
			name:   "no message",
			errStr: "error",
			want:   []string{"[development] *error* `````` ```"},
		},
		{
			name:     "with message",
			errStr:   "error",
			messages: []string{"message 1"},
			want:     []string{"[development] *error* ```message 1``` ```"},
		},
		{
			name:     "multiple messages",
			errStr:   "multiple errors",
			messages: []string{"message 1", "message 2"},
			want:     []string{"[development] *multiple errors* ```message 1", "", "message 2``` ```"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			CaptureWithStackTrace(tt.errStr, tt.messages...)
			var (
				file *os.File
				err  error
			)
			for {
				file, err = os.Open("example/panics.log")
				if err != nil {
					if os.IsNotExist(err) {
						time.Sleep(3 * time.Second)
						continue

					}
					t.Fatalf("failed to read file: %s", err.Error())
				}
				break
			}
			scanner := bufio.NewScanner(file)
			i := 0
			for scanner.Scan() {
				content := scanner.Text()
				if content != tt.want[i] {
					t.Errorf("unexpected file output got: %s, want: %s", content, tt.want[i])
				}
				i++
				if i >= len(tt.want) {
					break
				}
			}
			err = os.Remove("example/panics.log")
			if err != nil {
				t.Fatalf("failed to delete file: %s", err.Error())
			}
		})
	}

}

func TestCaptureGoroutine(t *testing.T) {
	type args struct {
		handleFn   func()
		recoveryFn func()
	}
	type want struct {
		handleFnCalledTimes   int
		recoveryFnCalledTimes int
	}

	handleFnCalledTimes := 0
	recoveryFnCalledTimes := 0
	done := make(chan bool)
	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "handleFn executed and not panic, shouldn't trigger recoveryFn",
			args: args{
				handleFn: func() {
					handleFnCalledTimes++
					done <- true
				},
				recoveryFn: func() {
					recoveryFnCalledTimes++
					done <- true
				},
			},
			want: want{
				handleFnCalledTimes:   1,
				recoveryFnCalledTimes: 0,
			},
		},
		{
			name: "handleFn executed and panic, should trigger recoveryFn",
			args: args{
				handleFn: func() {
					handleFnCalledTimes++
					panic("panic here")
				},
				recoveryFn: func() {
					recoveryFnCalledTimes++
					done <- true
				},
			},
			want: want{
				handleFnCalledTimes:   1,
				recoveryFnCalledTimes: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handleFnCalledTimes = 0
			recoveryFnCalledTimes = 0
			go CaptureGoroutine(tt.args.handleFn, tt.args.recoveryFn)
			select {
			case <-done:
			}
			if handleFnCalledTimes != tt.want.handleFnCalledTimes {
				t.Errorf("HandleFn was called %v times, expected %v times", handleFnCalledTimes, tt.want.handleFnCalledTimes)
			}
			if recoveryFnCalledTimes != tt.want.recoveryFnCalledTimes {
				t.Errorf("RecoveryFn was called %v times, expected %v times", recoveryFnCalledTimes, tt.want.recoveryFnCalledTimes)
			}
		})
	}
}
