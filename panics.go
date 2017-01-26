package panics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime/debug"

	"strings"

	"github.com/julienschmidt/httprouter"
)

var (
	env             string
	filepath        string
	slackWebhookURL string
	slackChannel    string
	tagString       string
)

type Tags map[string]string

type Options struct {
	Env             string
	Filepath        string
	SentryDSN       string
	SlackWebhookURL string
	SlackChannel    string
	Tags            Tags
}

func SetOptions(o *Options) {
	filepath = o.Filepath
	slackWebhookURL = o.SlackWebhookURL
	slackChannel = o.SlackChannel

	env = o.Env

	var tmp []string
	for key, val := range o.Tags {
		tmp = append(tmp, fmt.Sprintf("`%s: %s`", key, val))
	}
	tagString = strings.Join(tmp, " | ")
}

func init() {
	env = os.Getenv("TKPENV")
}

// CaptureHandler handle panic on http handler.
func CaptureHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			r := recover()

			if r != nil {
				switch t := r.(type) {
				case string:
					err = errors.New(t)
				case error:
					err = t
				default:
					err = errors.New("Unknown error")
				}

				publishError(err, request, true)

				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	}
}

// CaptureHTTPRouterHandler handle panic on httprouter handler.
func CaptureHTTPRouterHandler(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		var err error
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			r := recover()

			if r != nil {
				switch t := r.(type) {
				case string:
					err = errors.New(t)
				case error:
					err = t
				default:
					err = errors.New("Unknown error")
				}

				publishError(err, request, true)

				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}()
		h(w, r, ps)
	}
}

// CaptureNegroniHandler handle panic on negroni handler.
func CaptureNegroniHandler(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	var err error
	request, _ := httputil.DumpRequest(r, true)
	defer func() {
		r := recover()

		if r != nil {
			switch t := r.(type) {
			case string:
				err = errors.New(t)
			case error:
				err = t
			default:
				err = errors.New("Unknown error")
			}

			publishError(err, request, true)

			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}()
	next(w, r)
}

// Capture will publish any errors
func Capture(err string, message ...string) {
	var tmp string
	for i, val := range message {
		if i == 0 {
			tmp += val
		} else {
			tmp += fmt.Sprintf("\n\n%s", val)
		}
	}

	publishError(errors.New(err), []byte(tmp), false)
}

func publishError(errs error, reqBody []byte, withStackTrace bool) {
	var text string
	var snip string
	var buffer bytes.Buffer
	errorStack := debug.Stack()
	buffer.WriteString(fmt.Sprintf(`[%s] *%s*`, env, errs.Error()))

	if len(tagString) > 0 {
		buffer.WriteString(" | " + tagString)
	}

	if reqBody != nil {
		buffer.WriteString(fmt.Sprintf(" ```%s``` ", string(reqBody)))
	}
	text = buffer.String()

	if errorStack != nil && withStackTrace {
		snip = fmt.Sprintf("```\n%s```", string(errorStack))
	}

	if slackWebhookURL != "" {
		go postToSlack(buffer.String(), snip)
	}
	if filepath != "" {
		go func() {
			fp := fmt.Sprintf("%s/panics.log", filepath)
			file, err := os.OpenFile(fp, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
			if err != nil {
				log.Printf("[panics] failed to open file %s", fp)
				return
			}
			file.Write([]byte(text))
			file.Write([]byte(snip + "\r\n"))
			file.Close()
		}()
	}
}

func postToSlack(text, snip string) {
	payload := map[string]interface{}{
		"text": text,
		"attachments": []map[string]interface{}{
			map[string]interface{}{
				"text":      snip,
				"mrkdwn_in": []string{"text"},
			},
		},
	}
	if slackChannel != "" {
		payload["channel"] = slackChannel
	}
	b, _ := json.Marshal(payload)

	resp, err := http.DefaultClient.Post(slackWebhookURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		log.Printf("[panics] error on capturing error : %s \n", err.Error())
	}

	if resp.StatusCode >= 300 {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf("[panics] error on capturing error : %s \n", err)
			return
		}
		log.Printf("[panics] error on capturing error : %s \n", string(b))
	}
}
