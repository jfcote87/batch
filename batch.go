// Copyright 2015 James Cote and Liberty Fund, Inc.
// All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Batch implements a service to use the google client api
// batch protocol.  For specifics of the batch protocol see
// https://cloud.google.com/storage/docs/json_api/v1/how-tos/batch
package batch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/oauth2"

	"github.com/jfcote87/api/googleapi"
)

const batchURL = "https://www.googleapis.com/batch"

// Max number of entries in a batch call
const maxBatchEntries = 1000

var logFlag bool = true

// Credentialer returns an oauth2 access token
type Credentialer interface {
	Authorization() (string, error)
}

// Oauth2Credentials wraps an oauth2.TokenSource to make it a Credentialer
type Oauth2Credentials struct {
	oauth2.TokenSource
}

// Authorization returns the authorization string from the TokenSource
func (o *Oauth2Credentials) Authorization() (string, error) {
	tk, err := o.Token()
	if err != nil {
		return "", err
	}
	return tk.Type() + " " + tk.AccessToken, nil
}

// ServiceCredentials is used to indicate that the batch should use the
// Service client to authorize the call
var ServiceCredentials Credentialer = nil

// call stores data from a client api call and is used by the Batch Service to
// create individual parts in the batch call
type call struct {
	*googleapi.Call
	// extra data passed to Response to allow for processing after batch completion
	tag interface{}
	// if not nil then this credential  is used to override batch.Service credential data
	credentialer Credentialer
}

// getResult always returns a result generated from rd.  Errors are
// returned in the Result.Err field.
func (c call) getResult(rd io.Reader) Result {
	var ty reflect.Type
	r := &Result{Tag: c.tag}

	if val := reflect.ValueOf(c.Result); val.Kind() == reflect.Ptr && !val.IsNil() {
		ty = val.Elem().Type()
	}
	res, err := http.ReadResponse(bufio.NewReader(rd), nil)
	if err != nil {
		return r.setZeroValue(err, ty)
	}
	if err = googleapi.CheckResponse(res); err != nil {
		return r.setZeroValue(err, ty)
	}
	defer res.Body.Close()
	if ty != nil {
		if res.ContentLength == 0 {
			return r.setZeroValue(errors.New("batch: Empty body returned"), ty)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			return r.setZeroValue(fmt.Errorf("batch: Content-Type = %s; want \"application/json\"", ct), ty)
		}
		v := reflect.New(ty).Elem()
		r.Err = json.NewDecoder(res.Body).Decode(v.Addr().Interface())
		r.Value = v.Interface()
	}
	return *r
}

// Response returns results of a call
type Result struct {
	// Value is the json decode response
	Value interface{}
	// Tag is copied from corresponding Request.tag and is used to pass data
	// for further processing of the result.
	Tag interface{}
	// Err is the error result from a call
	Err error
}

// setZeroValue set the Result's Err field and sets the value to the
// zero value of ty
func (r *Result) setZeroValue(err error, ty reflect.Type) Result {
	r.Err = err
	if ty != nil {
		r.Value = reflect.Zero(ty).Interface()
	}
	return *r
}

// Service for submitting batch
type Service struct {
	// Is set to http.DefaultClient if nil.  An oauth2.Client may be used
	// to authorize the requests or each individual request may have its
	// own credential removing the need for an authorizing client.
	Client *http.Client
	// MaxCalls is the maximum number of calls that may be sent in a batch.
	// The value must be between 1 and 1000.  If zero, 1000 is assumed.
	MaxCalls int

	// mu protectes the callQueue
	mu        sync.Mutex
	callQueue []call
}

// Queue add a new call to the Service.callQueue using the error
// returned from an api Do() method.  If err is not a
// *googleapi.Call then the original error is returned.
//
// tag provides data to be used to process the call's result
//
// credential provides the credentialer that will be used for the
// queued call.  If nil, the batch.Service will use its default
// credentials.
//
// example:
//
// _, err = svCal.Events.Insert(*cal, eventData).Do()
// if err = batchService.Queue(err, "tag data", batch.ServiceCredentials) {
//	  // handle error
// }
func (s *Service) Queue(err error, tag interface{}, credential Credentialer) error {
	cx, ok := err.(*googleapi.Call)
	if !ok {
		if err == nil {
			return errors.New("batch: err is nil.")
		}
		return err
	}

	// Add to service request queue
	s.mu.Lock()
	defer s.mu.Unlock()

	s.callQueue = append(s.callQueue, call{
		Call:         cx,
		tag:          tag,
		credentialer: credential,
	})
	return nil
}

// Count returns number of requests currently batched
func (s *Service) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.callQueue)
}

// getCalls returns a slice of calls for processing.  The calls are removed
// from Service.callQueue.
func (s *Service) getCalls() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := len(s.callQueue)

	// Copy requests to local variable and reset s.requests
	// TODO: more eloquent error handling and setting of options
	if s.MaxCalls < 1 || s.MaxCalls > 1000 {
		s.MaxCalls = 1000
	}
	MaxCalls := s.MaxCalls

	calls := s.callQueue
	if l > MaxCalls {
		s.callQueue = make([]call, (l - MaxCalls))
		copy(s.callQueue, calls[MaxCalls:])
		calls = calls[:MaxCalls]
	} else {
		s.callQueue = make([]call, 0, MaxCalls)
	}
	return calls
}

// Do sends up to maxCalls(default 1000) in a single request.  Remaining requests
// must be processed in subsequent calls to Do.
func (s *Service) Do() ([]Result, error) {
	if len(s.callQueue) == 0 {
		return nil, errors.New("batch: No calls queued")
	}
	calls := s.getCalls()
	pr, boundary := createBody(calls)

	// Create req to send batches
	req, err := http.NewRequest("POST", batchURL, pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "multipart/mixed; boundary=\""+boundary+"\"")
	req.Header.Set("User-Agent", "google-api-go-batch 0.1")

	client := s.Client
	if client == nil {
		// using DefautClient is ok if each individual Requests have a credential
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if err = googleapi.CheckResponse(res); err != nil {
		return nil, err
	}
	defer googleapi.CloseBody(res)

	ct, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ct, "multipart/") {
		return nil, fmt.Errorf("batch: Invalid Content Type returned %s", ct)
	}
	boundary = params["boundary"]

	return processBody(multipart.NewReader(res.Body, boundary), calls)
}

func createBody(calls []call) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	batchWriter := multipart.NewWriter(pw)

	go func() {
		var werr error
		var ptw io.Writer
		var body []byte //*bytes.Buffer
		var curTag interface{}

		defer func() {
			if werr != nil {
				pr.CloseWithError(fmt.Errorf("batch: (%v) createBody Error: %v", curTag, werr))
			}
			batchWriter.Close()
			pw.Close()
		}()

		for cnt, c := range calls {
			curTag = c.tag
			params := c.GetParams()
			if params.Get("alt") != "" {
				params.Set("alt", "json")
			}
			if ptw, werr = batchWriter.CreatePart(textproto.MIMEHeader{
				"Content-Type": []string{"application/http"},
				"Content-Id":   []string{fmt.Sprintf("batch%04d", cnt)},
			}); werr != nil {
				break
			}

			hdr := &bytes.Buffer{}
			hdr.WriteString(c.Method + " " + c.URL.Path + "?" + params.Encode() + " HTTP/1.1\r\n")
			if c.credentialer != nil {
				auth, werr := c.credentialer.Authorization()
				if werr != nil {
					break
				}
				hdr.WriteString("authorization: " + auth + "\r\n")
			}

			if c.Payload != nil {
				body, werr = json.Marshal(c.Payload)
				if werr == nil {
					hdr.WriteString("content-length: " + strconv.Itoa(len(body)) + "\r\ncontent-type: application/json\r\n\r\n")
					if _, werr = ptw.Write(hdr.Bytes()); werr == nil {
						_, werr = ptw.Write(body)
					}
				}
			} else {
				hdr.WriteString("\r\n")
				_, werr = ptw.Write(hdr.Bytes())
			}
			if werr != nil {
				break
			}
		}
	}()
	return pr, batchWriter.Boundary()
}

// processBody loops through requests and processes each part of multipart response
func processBody(mr *multipart.Reader, calls []call) ([]Result, error) {
	var err error
	var pr *multipart.Part
	results := make([]Result, 0, len(calls))
	cnt := 0
	for pr, err = mr.NextPart(); err == nil && cnt < len(calls); pr, err = mr.NextPart() {
		c := calls[cnt]
		cnt++
		if ct := pr.Header.Get("Content-Type"); ct != "application/http" {
			results = append(results, Result{Tag: c.tag,
				Err: fmt.Errorf("batch: Part content-type = %s; want application/http", ct)})
			continue
		}

		results = append(results, c.getResult(pr))
	}

	if err != io.EOF {
		if cnt < len(calls) {
			for _, c := range calls[cnt:] {
				results = append(results, Result{Tag: c.tag, Err: err})
			}
		}
		if err == nil {
			err = errors.New("batch: More results returned than calls")
		}
	} else {
		err = nil
	}
	return results, err
}
