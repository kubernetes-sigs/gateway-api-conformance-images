/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	// Test a valid health check
	req, err := http.NewRequest("GET", "/health", nil) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(healthHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Expected status code %d, but got %d", http.StatusOK, status)
	}

	expectedResponse := "OK"
	if rr.Body.String() != expectedResponse {
		t.Errorf("Expected response body %s, but got %s", expectedResponse, rr.Body.String())
	}
}

func TestDelayResponse(t *testing.T) {
	// Test with a valid delay integer value
	req, err := http.NewRequest("GET", "/?delay=1s", nil) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}

	err = delayResponse(req, "delay")
	if err != nil {
		t.Errorf("Expected no error, but got %v", err)
	}

	// Test with a valid delay decimal value
	req, err = http.NewRequest("GET", "/?delay=0.1s", nil) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}

	err = delayResponse(req, "delay")
	if err != nil {
		t.Errorf("Expected no error, but got %v", err)
	}

	// Test with an invalid delay value
	req, err = http.NewRequest("GET", "/?delay=invalid", nil) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}

	err = delayResponse(req, "delay")
	if err == nil {
		t.Errorf("Expected an error, but got nil")
	}
}

func TestStatusHandler(t *testing.T) {
	// Test with a valid status code (200)
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/status/200", nil)
	rr := httptest.NewRecorder()

	statusHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Expected status code %d, but got %d", http.StatusOK, status)
	}

	// Test with a valid status code (404)
	req = httptest.NewRequestWithContext(t.Context(), "GET", "/status/404", nil)
	rr = httptest.NewRecorder()

	statusHandler(rr, req)

	if status := rr.Code; status != http.StatusNotFound {
		t.Errorf("Expected status code %d, but got %d", http.StatusNotFound, status)
	}

	// Test with a valid status (305)
	req = httptest.NewRequestWithContext(t.Context(), "GET", "/status/305", nil)
	rr = httptest.NewRecorder()

	statusHandler(rr, req)

	if status := rr.Code; status != http.StatusUseProxy {
		t.Errorf("Expected status code %d, but got %d", http.StatusUseProxy, status)
	}

	// Test with an invalid status code (string)
	req = httptest.NewRequestWithContext(t.Context(), "GET", "/status/invalid", nil)
	rr = httptest.NewRecorder()

	statusHandler(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("Expected status code %d, but got %d", http.StatusBadRequest, status)
	}
}

func TestEchoHandler(t *testing.T) {
	// Create an HTTP request to the / endpoint
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	rr := httptest.NewRecorder()

	// Set up the context
	context = Context{
		Namespace: "testNamespace",
		Ingress:   "testIngress",
		Service:   "testService",
		Pod:       "testPod",
	}

	echoHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Expected status code %d, but got %d", http.StatusOK, status)
	}

	// Test response headers have correct ContentType
	expectedContentType := "application/json"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("Expected Content-Type header %s, but got %s", expectedContentType, contentType)
	}

	// Test response headers have correct ContentTypeOptions
	expectedXContentTypeOptions := "nosniff"
	if xContentTypeOptions := rr.Header().Get("X-Content-Type-Options"); xContentTypeOptions != expectedXContentTypeOptions {
		t.Errorf("Expected X-Content-Type-Options header %s, but got %s", expectedXContentTypeOptions, xContentTypeOptions)
	}

	// Test the response body by unmarshalling it into a RequestAssertions struct
	var responseAssertions RequestAssertions
	err := json.Unmarshal(rr.Body.Bytes(), &responseAssertions)
	if err != nil {
		t.Errorf("Error unmarshalling response body: %v", err)
	}

	// Test RequestAssertions struct contains expected path
	expectedPath := "/"
	if responseAssertions.Path != expectedPath {
		t.Errorf("Expected Path %s, but got %s", expectedPath, responseAssertions.Path)
	}

	// Test RequestAssertions struct contains expected context namespace
	expectedNamespace := context.Namespace
	if responseAssertions.Namespace != expectedNamespace {
		t.Errorf("Expected X-Content-Type-Options header %s, but got %s", expectedNamespace, responseAssertions.Namespace)
	}
}

func TestEchoHandler_RequestBodySHA256(t *testing.T) {
	sha256Hex := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}

	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "no body", method: http.MethodGet, wantStatus: http.StatusOK},
		{name: "json body", method: http.MethodPost, contentType: "application/json", body: `{"model":"m","prompt":"p"}`, wantStatus: http.StatusOK},
		// The form body is still parsed for ?delay while the digest is taken.
		{name: "form body with delay", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", body: "delay=1ms", wantStatus: http.StatusOK},
		{name: "form body with invalid delay", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", body: "delay=invalid", wantStatus: http.StatusInternalServerError},
		// The form parser stops at 10 MB, so the digest must also cover the bytes it never read.
		{name: "form body over the form parser's limit", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", body: "a=" + strings.Repeat("x", 11<<20), wantStatus: http.StatusOK},
		{name: "multipart body with delay", method: http.MethodPost, contentType: "multipart/form-data; boundary=b", body: "--b\r\nContent-Disposition: form-data; name=\"delay\"\r\n\r\n1ms\r\n--b--\r\n", wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, "/", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rr := httptest.NewRecorder()

			echoHandler(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("Expected status code %d, but got %d", tc.wantStatus, rr.Code)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if got, want := rr.Header().Get(requestBodySHA256Header), sha256Hex(tc.body); got != want {
				t.Errorf("Expected %s header %s, but got %s", requestBodySHA256Header, want, got)
			}
		})
	}
}

// A client that asks the server to echo its own digest header, in another case, must still read only the server's digest.
// The check goes through a real HTTP server, because the recorder keeps the two header names apart.
func TestEchoHandler_RequestBodySHA256IgnoresClientSetHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	body := `{"prompt":"p"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Echo-Set-Header", "X-Echo-Request-Body-SHA256:bogus")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sum := sha256.Sum256([]byte(body))
	if got, want := resp.Header.Values(requestBodySHA256Header), []string{hex.EncodeToString(sum[:])}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Expected %s values %v, but got %v", requestBodySHA256Header, want, got)
	}
}

// A form body that ends before its Content-Length must fail, even though form parsing drops the read error.
// The check needs a real connection, because only the server's body reader turns the early end into a later EOF.
func TestEchoHandler_TruncatedFormBodyFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: echo\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\na=b"); err != nil {
		t.Fatal(err)
	}
	if err = conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, but got %d", http.StatusInternalServerError, resp.StatusCode)
	}
	if got := resp.Header.Get(requestBodySHA256Header); got != "" {
		t.Errorf("Expected no %s header on the error response, but got %s", requestBodySHA256Header, got)
	}
}

// A client that sends Expect: 100-continue and waits must get an early error without uploading the body.
func TestEchoHandler_EarlyErrorWithExpectContinue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer server.Close()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(conn, "POST /?delay=invalid HTTP/1.1\r\nHost: echo\r\nContent-Type: application/json\r\nContent-Length: 10\r\nExpect: 100-continue\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("Expected the error response before the body is sent, but got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, but got %d", http.StatusInternalServerError, resp.StatusCode)
	}
	if got := resp.Header.Get(requestBodySHA256Header); got != "" {
		t.Errorf("Expected no %s header on the error response, but got %s", requestBodySHA256Header, got)
	}
}

// A retry request whose upload never finishes must not hold up a request for another uuid.
// Both have succeedAfter=0, so both go straight to echoHandler, which reads the body.
func TestRetrySimulation_SlowUploadDoesNotBlockOtherRequests(t *testing.T) {
	sim := &retrySimulation{attempts: make(map[string]int)}
	// The slow request's body reports its first read, which happens inside echoHandler, so the test knows the handler is past the lock.
	reading := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uuid") == "slow" {
			r.Body = &firstReadSignal{ReadCloser: r.Body, signal: func() { once.Do(func() { close(reading) }) }}
		}
		sim.retrySimulationHandler(w, r)
	}))
	defer server.Close()

	stalled, stalledWriter := io.Pipe()
	defer stalledWriter.Close()
	slowReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/retry/?uuid=slow&succeedAfter=0", stalled)
	if err != nil {
		t.Fatal(err)
	}
	slowReq.Header.Set("Content-Type", "application/json")
	go func() {
		if slowResp, slowErr := server.Client().Do(slowReq); slowErr == nil {
			slowResp.Body.Close()
		}
	}()
	// Part of the body is sent, and the other request waits until the server is reading it.
	if _, writeErr := stalledWriter.Write([]byte(`{"prompt":`)); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case <-reading:
	case <-time.After(5 * time.Second):
		t.Fatal("Expected the server to start reading the slow request's body")
	}

	otherReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/retry/?uuid=other&succeedAfter=0", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	otherReq.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Do(otherReq)
	if err != nil {
		t.Fatalf("Expected the other request to complete while the slow upload is stalled, but got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, but got %d", http.StatusOK, resp.StatusCode)
	}
}

// firstReadSignal calls signal before the first read of the wrapped body.
type firstReadSignal struct {
	io.ReadCloser
	signal func()
}

func (f *firstReadSignal) Read(p []byte) (int, error) {
	f.signal()
	return f.ReadCloser.Read(p)
}

func TestWriteEchoResponseHeaders(t *testing.T) {
	// Create a response recorder to capture the response
	rr := httptest.NewRecorder()
	headers := http.Header{}

	// Test with a single header
	headers.Add("X-Echo-Set-Header", "Header1:Value1")

	writeEchoResponseHeaders(rr, headers)

	// Test response headers, check header is correct
	expectedHeaderName := "Header1"
	expectedHeaderValue := "Value1"
	if headerValues := rr.Header().Values(expectedHeaderName); len(headerValues) != 1 || headerValues[0] != expectedHeaderValue {
		t.Errorf("Expected header %s:%s, but got %s:%s", expectedHeaderName, expectedHeaderValue, headerValues[0], headerValues[1])
	}

	// Test with multiple headers separated by commas
	rr = httptest.NewRecorder()
	headers = http.Header{}
	headers.Add("X-Echo-Set-Header", "Header2:Value2, Header3:Value3")

	writeEchoResponseHeaders(rr, headers)

	// Test response headers, check headers are correct
	expectedHeaderName2 := "Header2"
	expectedHeaderValue2 := "Value2"
	expectedHeaderName3 := "Header3"
	expectedHeaderValue3 := "Value3"
	headerValues2 := rr.Header().Values(expectedHeaderName2)
	headerValues3 := rr.Header().Values(expectedHeaderName3)
	if len(headerValues2) != 1 || headerValues2[0] != expectedHeaderValue2 ||
		len(headerValues3) != 1 || headerValues3[0] != expectedHeaderValue3 {
		t.Errorf("Expected header %s:%s and header %s:%s, but got %s:%s and %s:%s",
			expectedHeaderName2, expectedHeaderValue2, expectedHeaderName3, expectedHeaderValue3,
			headerValues2[0], headerValues2[1], headerValues3[0], headerValues3[1])
	}

	// Test empty header
	rr = httptest.NewRecorder()
	headers = http.Header{}
	headers.Add("X-Echo-Set-Header", "")

	writeEchoResponseHeaders(rr, headers)

	// Test response headers is not set
	headerValuesEmpty := rr.Header().Values(expectedHeaderName)
	if len(headerValuesEmpty) != 0 {
		t.Errorf("Expected no header %s, but got %s:%s", expectedHeaderName, expectedHeaderName, headerValuesEmpty[0])
	}
}

func TestEchoHandler_IncludesHTTPPort(t *testing.T) {
	// Preserve global across tests.
	prevHTTPPort := httpPort
	defer func() { httpPort = prevHTTPPort }()

	httpPort = "3002"

	// Minimal context to satisfy marshaling (values don’t matter for this check).
	context = Context{
		Namespace: "testNamespace",
		Ingress:   "testIngress",
		Service:   "testService",
		Pod:       "testPod",
	}

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	rr := httptest.NewRecorder()

	echoHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, status)
	}

	var resp RequestAssertions
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal echo response: %v", err)
	}
	if resp.HTTPPort != "3002" {
		t.Errorf("expected HTTPPort %q, got %q", "3002", resp.HTTPPort)
	}
}

func TestEchoHandler_HTTPPortReflectsChange(t *testing.T) {
	// Preserve global across tests.
	prevHTTPPort := httpPort
	defer func() { httpPort = prevHTTPPort }()

	httpPort = "3004"
	context = Context{
		Namespace: "ns",
		Ingress:   "ing",
		Service:   "svc",
		Pod:       "pod",
	}

	req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	rr := httptest.NewRecorder()

	echoHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, status)
	}

	var resp RequestAssertions
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal echo response: %v", err)
	}
	if resp.HTTPPort != "3004" {
		t.Errorf("expected HTTPPort %q, got %q", "3004", resp.HTTPPort)
	}
}

func TestProcessError(t *testing.T) {
	// Create a response recorder to capture the response
	rr := httptest.NewRecorder()
	err := fmt.Errorf("Test error message")
	code := http.StatusInternalServerError

	processError(rr, err, code)

	// Test response status code is StatusInternalServerError
	if status := rr.Code; status != code {
		t.Errorf("Expected status code %d, but got %d", code, status)
	}

	// Test Content-Type header
	expectedContentType := "application/json"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("Expected Content-Type header %s, but got %s", expectedContentType, contentType)
	}

	// Test X-Content-Type-Options header
	expectedXContentTypeOptions := "nosniff"
	if xContentTypeOptions := rr.Header().Get("X-Content-Type-Options"); xContentTypeOptions != expectedXContentTypeOptions {
		t.Errorf("Expected X-Content-Type-Options header %s, but got %s", expectedXContentTypeOptions, xContentTypeOptions)
	}

	// Test the response body by unmarshalling it into a responseBody struct
	var responseBody struct {
		Message string `json:"message"`
	}
	err = json.Unmarshal(rr.Body.Bytes(), &responseBody)
	if err != nil {
		t.Errorf("Error unmarshalling response body: %v", err)
	}

	// Test response body error message
	expectedErrorMessage := "Test error message"
	if responseBody.Message != expectedErrorMessage {
		t.Errorf("Expected error message '%s', but got '%s'", expectedErrorMessage, responseBody.Message)
	}
}

func TestProcessErrorWithJSONError(t *testing.T) {
	// Create a response recorder to capture the response
	rr := httptest.NewRecorder()
	err := json.Unmarshal([]byte(`testing invalid JSON`), new(any))
	code := http.StatusInternalServerError

	processError(rr, err, code)

	// Test response status code is StatusInternalServerError
	if status := rr.Code; status != code {
		t.Errorf("Expected status code %d, but got %d", code, status)
	}

	// Test Content-Type header
	expectedContentType := "application/json"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("Expected Content-Type header %s, but got %s", expectedContentType, contentType)
	}

	// Test X-Content-Type-Options header
	expectedXContentTypeOptions := "nosniff"
	if xContentTypeOptions := rr.Header().Get("X-Content-Type-Options"); xContentTypeOptions != expectedXContentTypeOptions {
		t.Errorf("Expected X-Content-Type-Options header %s, but got %s", expectedXContentTypeOptions, xContentTypeOptions)
	}

	// Test the response body by unmarshalling it into a responseBody struct
	var responseBody struct {
		Message string `json:"message"`
	}
	err = json.Unmarshal(rr.Body.Bytes(), &responseBody)
	if err != nil {
		t.Errorf("Error unmarshalling response body: %v", err)
	}

	// Test response body error message
	if !strings.Contains(responseBody.Message, "invalid character") {
		t.Errorf("Expected error message to contain 'invalid character', but got '%s'", responseBody.Message)
	}
}

func TestTLSStateToAssertions(t *testing.T) {
	// Create a mock TLS connection state for testing
	mockState := &tls.ConnectionState{
		Version:            tls.VersionTLS13,
		NegotiatedProtocol: "http/2",
		ServerName:         "test-example.com",
		CipherSuite:        tls.TLS_AES_256_GCM_SHA384,
		PeerCertificates:   []*x509.Certificate{},
	}

	// Call the function to convert the mock state
	result := tlsStateToAssertions(mockState)

	// Define expected TLS state
	expected := &TLSAssertions{
		Version:            "TLSv1.3",
		NegotiatedProtocol: "http/2",
		ServerName:         "test-example.com",
		CipherSuite:        "TLS_AES_256_GCM_SHA384",
		PeerCertificates:   []string{},
	}

	// Test the converted result is matching the expected
	if !compareTLSAssertions(result, expected) {
		t.Errorf("Result does not match expected values.\nGot: %+v\nExpected: %+v", result, expected)
	}
}

func compareTLSAssertions(a, b *TLSAssertions) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Version == b.Version &&
		a.NegotiatedProtocol == b.NegotiatedProtocol &&
		a.ServerName == b.ServerName &&
		a.CipherSuite == b.CipherSuite &&
		strings.Join(a.PeerCertificates, "") == strings.Join(b.PeerCertificates, "")
}
