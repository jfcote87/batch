// Copyright 2015 James Cote and Liberty Fund, Inc.
// All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package batch

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"reflect"
	"testing"

	"github.com/jfcote87/api/googleapi"
)

type Tstr struct {
	A string `json:"a,omitempty"`
	B int    `json:"b,omitempty"`
}

type testData struct {
	d       string
	status  int
	isError bool
	msg     string
	h       string
}

type testError struct {
	E error
}

func (e testError) Error() string {
	return e.E.Error()
}

type bodyReadCloser struct {
	*bytes.Reader
}

func (b *bodyReadCloser) Close() error {
	return nil
}

var returnTstr *Tstr

//  JSON Response Tests
func TestGetResult(t *testing.T) {
	// test empty call
	b := getTestResponseBody("", 200)
	c := call{Call: &googleapi.Call{}}
	if res := c.getResult(bytes.NewReader(b)); res.Err != nil {
		t.Fatalf("getResult: empty call object - Result.Err = (%v) %v; wanted nil.", reflect.TypeOf(res.Err), res.Err)
	}

	// 0 length response with a result type should produce error
	c.Result = &returnTstr
	if res := c.getResult(bytes.NewReader(b)); res.Err == nil || res.Err.Error() != "batch: Empty body returned" {
		t.Fatalf("getResult: Result.Err = (%v) %v; wanted batch: Empty body returned.", reflect.TypeOf(res.Err), res.Err)
	}

	// test correct call
	testData := `{"b": 5, "a":"Teststring"}`
	b = getTestResponseBody(testData, 200)
	if res := c.getResult(bytes.NewReader(b)); res.Err != nil {
		t.Errorf("getResult: Result.Err = %v; wanted nil", res.Err)
	} else {
		if v, ok := res.Value.(*Tstr); !ok {
			t.Errorf("getResult: r.Value should is type %v; wanted *Tstr", reflect.TypeOf(res.Value))
		} else {
			if v.B != 5 || v.A != "Teststring" {
				t.Errorf("getResult: Result.Value = %#v; wanted %s", v, testData)
			}
		}
	}

	// test invalid http format
	if res := c.getResult(bytes.NewBufferString("X" + string(b))); res.Err == nil {
		t.Errorf("getResult: Result.Err is nil; wanted  malformed HTTP version error")
	} else {
		t.Logf("Err: %v", res.Err)
	}

	// test status code != 200
	b = getTestResponseBody(testData, 500)
	if res := c.getResult(bytes.NewReader(b)); res.Err == nil {
		t.Errorf("getResult: Result.Err is nil; wanted googleapi: got HTTP response code 500")
	} else {
		t.Logf("Err: %v", res.Err)
	}

	// test json error
	testData = `{"a": 5, "b":"Teststring"}`
	b = getTestResponseBody(testData, 200)
	if res := c.getResult(bytes.NewReader(b)); res.Err == nil {
		t.Errorf("getResult: Result.Err is nil; wanted json parse error")
	}

}

func getTestResponseBody(data string, statusCode int) []byte {
	testBody := bytes.NewBuffer(make([]byte, 0))
	res := &http.Response{StatusCode: statusCode, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header)}
	if data != "" {
		b := &bodyReadCloser{Reader: bytes.NewReader([]byte(data))}
		res.ContentLength = int64(b.Reader.Len())
		res.Body = b
		res.Header.Set("Content-Type", "application/json")
	}
	_ = res.Write(testBody)
	return testBody.Bytes()
}

func TestResultSetZeroValue(t *testing.T) {
	r := &Result{}
	ptr := &Tstr{}
	ty := reflect.TypeOf(ptr)
	r.setZeroValue(errors.New("test error"), ty)
	if r.Err.Error() != "test error" {
		t.Errorf("setZeroValue: r.Err = %v; wanted \"test error\".", r.Err)
	}
	if ptr, ok := r.Value.(*Tstr); !ok {
		t.Errorf("setZeroValue:, r.Value = %v %v; wanted <*batch.Tstr Value> nil.", reflect.ValueOf(r.Value), r.Value)
	} else {
		if ptr != nil {
			t.Errorf("setZeroValue:, r.Value = %v; wanted nil.", ptr)
		}
	}
}

func TestProcessBody(t *testing.T) {
	//ty := reflect.TypeOf(&Tstr{})
	tData := []testData{
		testData{d: `{"a": "String 0", "b": 0}`, status: 200, isError: false, msg: "unexpected err = %v"},
		testData{d: `{"a": "String 1", "b": 1}`, status: 500, isError: true, msg: "should return googleapi error"},
		testData{d: `{"a": 0000, "b": 2}`, status: 200, isError: true, msg: "invalid json should return json error"},
		testData{status: 200, msg: "unexpected err = %v"},
	}
	calls := []call{
		call{tag: 0, Call: &googleapi.Call{Result: &returnTstr}},
		call{tag: 1, Call: &googleapi.Call{Result: &returnTstr}},
		call{tag: 2, Call: &googleapi.Call{Result: &returnTstr}},
		call{tag: 4, Call: &googleapi.Call{}},
	}
	buf := bytes.NewBuffer(make([]byte, 0))
	mp := multipart.NewWriter(buf)
	boundary := mp.Boundary()
	for _, d := range tData {
		pw, _ := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type": []string{"application/http"},
			"Content-Id":   []string{"batch01"},
		})
		_, _ = io.Copy(pw, bytes.NewReader(getTestResponseBody(d.d, d.status)))
	}
	_ = mp.Close()

	mr := multipart.NewReader(buf, boundary)

	resultSlice, err := processBody(mr, calls)
	if err != nil {
		t.Fatalf("processBody: %v", err)
		return
	}
	for i, r := range resultSlice {
		if tData[i].isError {
			if r.Err == nil {
				t.Errorf("processBody: Test#%d "+tData[i].msg, i)
			}
		} else {
			if r.Err != nil {
				t.Errorf("processBody: Test#%d "+tData[i].msg, i, err)
			} else if tData[i].d != "" {
				if tval, ok := r.Value.(*Tstr); !ok {
					t.Errorf("processBody: Test#%d r.Value is type %v; wanted *Tst", i, reflect.TypeOf(r.Value))
				} else {
					if tval.B != i {
						t.Errorf("processBody: Test#%d res.Value.B = %d; wanted %d", i, tval.B, i)
					}
				}

			}
		}
	}
}

func TestQueueMethod(t *testing.T) {
	sv := &Service{}
	if err := sv.Queue(nil, nil, nil); err == nil {
		t.Errorf("Queue: sv.Queue(nil) = %v; wanted batch: err is nil.", err)
	}
	testErr := errors.New("This is not a batch.call")
	if err := sv.Queue(testErr, nil, nil); err != testErr {
		t.Errorf("Queue: sv.Queue(testErr) = %v; wanted testErr", err)
	}

	c := &googleapi.Call{Result: &returnTstr}
	if err := sv.Queue(c, 0, nil); err != nil {
		t.Errorf("Queue: sv.Queue(payload) = %v; wanted nil.", err)
	}
	if err := sv.Queue(c, 1, &Oauth2Credentials{}); err != nil {
		t.Errorf("Queue: sv.Queue(payload) = %v; wanted nil.", err)
	}
	if sv.Count() != 2 {
		t.Errorf("Queue: sv.Count() = %d; wanted 3.", sv.Count())
	} else {
		if c := sv.callQueue[0]; c.tag != 0 || reflect.TypeOf(&returnTstr) != reflect.TypeOf(c.Result) {
			t.Errorf("Queue: c.tag = %d, TypeOf(c.Result) = %v; wanted 0, %v.", c.tag, reflect.TypeOf(c.Result), reflect.TypeOf(&returnTstr))
		}
		if sv.callQueue[1].credentialer == nil {
			t.Errorf("Queue: c.credential = nil; wanted Oauth2Credentils{}")
		}
	}
}

/*
func TestAddRequest(t *testing.T) {
	err := fmt.Errorf("This is not a batch.Request")
	sv := &Service{}
	if err = sv.AddRequest(err); err == nil {
		t.Errorf("Add Request should have failed with a non batch.Request error")
	}

	d := &requestData{}
	b := &Request{data: d}
	var tx *Tstr
	if err = SetResult(tx)(b); err == nil {
		t.Errorf("Set result to a *ptr should error.  Need a **ptr")
	}
	if err = SetResult(&tx)(b); err != nil {
		t.Errorf("SetResult Fail: %v", err)
	} else {
		if testPtr, ok := b.resultPtr.(**Tstr); !ok || testPtr != &tx {
			t.Errorf("SetResult Fail: Pointer not initialized properly")
		}
	}
	//t.Errorf("%v", err)
	sv.Client = &http.Client{Transport: &TestTransport{}}
	sv.MaxRequests = 2

	d.method = "GET"
	d.uri = "/example/uri"
	d.header = make(http.Header)
	d.header.Set("A", "B")
	d.header.Set("D", "F")
	d.body = []byte("THIS IS A BODY")
	b.status = requestStatusQueued

	err = sv.AddRequest(d, SetResult(&tx), SetTag("1st Request"))
	if err != nil {
		t.Errorf("AddRequst Failed: %v", err)
	}
	err = sv.AddRequest(d, SetTag("2nd Request"))
	err = sv.AddRequest(d, SetTag("3rd Request"))
	if len(sv.requests) != 3 {
		t.Errorf("AddRequest count should be 3 instead is %d", len(sv.requests))
		return
	}

	_, err = sv.Do()
	// make certain we went thru ErrorTransport
	err, ok := err.(*url.Error)
	if !ok || err.(*url.Error).Err.Error() != "Test Transport" {
		t.Errorf("AddRequest: TestTransport error %v", err)
	}

	// Ensure that on MaxRequests were sent and that remaining were still stored
	if len(sv.requests) != 1 {
		for i, rx := range sv.requests {

			log.Printf("Request %d: %v\n", i, rx.tag)
		}
		t.Errorf("AddRequest count should be 1 instead is %d", len(sv.requests))
	}
	//b = &request

}

type TestTransport struct{}

func (tr *TestTransport) RoundTrip(req *http.Request) (res *http.Response, err error) {
	b := bytes.NewBuffer(make([]byte, 0, req.ContentLength))
	io.Copy(b, req.Body)
	req.Body.Close()
	//log.Printf("%s\n", string(b.Bytes()))
	return nil, fmt.Errorf("Test Transport")
}
*/
