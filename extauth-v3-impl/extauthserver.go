/*
Copyright The Kubernetes Authors.

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

// implements a conformance test server that speaks both the Envoy ext_authz
// v3 gRPC protocol and the HTTP external-auth protocol.
//
// Authorization logic: requests whose path has the prefix /grpc/allowed or
// /http/allowed are permitted; all other paths are denied with a 403 /
// PermissionDenied response.
//
// Every response (OK and denial, HTTP and gRPC) carries the following headers:
//
//	X-Auth-Received-Body-Size: <N>
//	    Number of body bytes the auth server received. Lets conformance tests
//	    verify that forwardBody.maxSize is honored: expect N == sent size when
//	    forwarding is enabled, and N == 0 when maxSize is 0.
//
// Additional headers on OK responses only:
//
//	X-User-Id: 42
//	    Fixed header; lets conformance tests verify allowedResponseHeaders
//	    forwarding to the upstream backend.
//
//	X-Auth-Received-<Suffix>: <value>
//	    Echo of each client request header named X-<Suffix>. Lets conformance
//	    tests verify which headers were forwarded to the auth server via the
//	    allowedHeaders filter option.
//
// Additional headers on denial responses only:
//
//	X-Auth-Error: access-denied
//	    Fixed header; lets conformance tests verify that auth server denial
//	    headers are passed through to the client.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

const (
	grpcAllowedPrefix = "/grpc/allowed"
	httpAllowedPrefix = "/http/allowed"
	deniedBody        = "Access denied by external auth server"
)

type grpcAuthServer struct {
	authv3.UnimplementedAuthorizationServer
}

func (s *grpcAuthServer) Check(_ context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	attrs := req.GetAttributes().GetRequest().GetHttp()
	path := attrs.GetPath()
	fmt.Printf("grpc check: path=%q\n", path)

	// body and raw_body are mutually exclusive: one is populated, the other is empty.
	bodySize := len(attrs.GetBody()) + len(attrs.GetRawBody())
	bodySizeStr := strconv.Itoa(bodySize)

	// Build OK response headers: fixed user-id, body-size report, and X-* echoes.
	hdrs := attrs.GetHeaders()
	okHdrs := make([]*corev3.HeaderValueOption, 0, len(hdrs)+2)
	okHdrs = append(okHdrs,
		&corev3.HeaderValueOption{Header: &corev3.HeaderValue{Key: "X-User-Id", Value: "42"}},
		&corev3.HeaderValueOption{Header: &corev3.HeaderValue{Key: "X-Auth-Received-Body-Size", Value: bodySizeStr}},
	)
	// Echo X-* request headers back as X-Auth-Received-* in OkHttpResponse.Headers
	// so that the gateway adds them to the upstream request. gRPC/HTTP2 headers are
	// lowercased, so canonicalize before checking the prefix.
	for key, val := range hdrs {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if suffix, ok := strings.CutPrefix(canonical, "X-"); ok {
			okHdrs = append(okHdrs, &corev3.HeaderValueOption{
				Header: &corev3.HeaderValue{
					Key:   "X-Auth-Received-" + suffix,
					Value: val,
				},
			})
		}
	}

	if strings.HasPrefix(path, grpcAllowedPrefix) {
		return &authv3.CheckResponse{
			Status: &statuspb.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{
				OkResponse: &authv3.OkHttpResponse{
					Headers: okHdrs,
				},
			},
		}, nil
	}

	return &authv3.CheckResponse{
		Status: &statuspb.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode_Forbidden},
				Headers: []*corev3.HeaderValueOption{
					{Header: &corev3.HeaderValue{Key: "X-Auth-Error", Value: "access-denied"}},
					{Header: &corev3.HeaderValue{Key: "X-Auth-Received-Body-Size", Value: bodySizeStr}},
				},
				Body: deniedBody,
			},
		},
	}, nil
}

func httpAuthHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	fmt.Printf("http check: path=%q\n", path)

	// Measure the forwarded body. ReadAll is safe when Body is nil.
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
	}
	bodySizeStr := strconv.Itoa(len(bodyBytes))

	if strings.HasPrefix(path, httpAllowedPrefix) {
		// Echo X-* request headers back as X-Auth-Received-* response headers so
		// that the gateway can forward them to the upstream via allowedResponseHeaders.
		// Go's net/http canonicalizes header names, so the "X-" prefix check is safe.
		for key, vals := range r.Header {
			if suffix, ok := strings.CutPrefix(key, "X-"); ok {
				w.Header().Set("X-Auth-Received-"+suffix, strings.Join(vals, ","))
			}
		}
		w.Header().Set("X-User-Id", "42")
		w.Header().Set("X-Auth-Received-Body-Size", bodySizeStr)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("X-Auth-Error", "access-denied")
	w.Header().Set("X-Auth-Received-Body-Size", bodySizeStr)
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, deniedBody)
}

func envPort(envVar string, defaultPort int) int {
	if v := os.Getenv(envVar); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			fmt.Printf("non-integer value in %s %q: %v\n", envVar, v, err)
			os.Exit(1)
		}
		return p
	}
	return defaultPort
}

func main() {
	grpcPort := envPort("GRPC_PORT", 9001)
	httpPort := envPort("HTTP_PORT", 9002)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// Start gRPC ext_authz server.
	lc := net.ListenConfig{}
	grpcLis, err := lc.Listen(ctx, "tcp", fmt.Sprintf("0.0.0.0:%d", grpcPort))
	if err != nil {
		stop()
		log.Fatalf("grpc: failed to listen: %v\n", err)
	}
	grpcServer := grpc.NewServer()
	authv3.RegisterAuthorizationServer(grpcServer, &grpcAuthServer{})
	fmt.Printf("grpc ext_authz server listening on %s\n", grpcLis.Addr())
	go func() {
		if err := grpcServer.Serve(grpcLis); err != nil {
			stop()
			log.Fatalf("grpc: serve error: %v\n", err)
		}
	}()

	// Start HTTP external-auth server.
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpAuthHandler)
	httpServer := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", httpPort), Handler: mux} //nolint:gosec
	fmt.Printf("http ext_authz server listening on %s\n", httpServer.Addr)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			stop()
			log.Fatalf("http: serve error: %v\n", err)
		}
	}()

	<-ctx.Done()
	grpcServer.GracefulStop()
	_ = httpServer.Shutdown(context.Background()) //nolint:contextcheck
	fmt.Print("listeners closed")
	stop()
}
