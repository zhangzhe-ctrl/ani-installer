// ANI environment probe client. It is not a proxy or a product tenant service.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

type wireCodec struct{}

func (wireCodec) Name() string { return "proto" }
func (wireCodec) Marshal(v any) ([]byte, error) {
	value, ok := v.([]byte)
	if !ok { return nil, fmt.Errorf("expected protobuf wire bytes") }
	return value, nil
}
func (wireCodec) Unmarshal(data []byte, v any) error {
	value, ok := v.(*[]byte)
	if !ok { return fmt.Errorf("expected protobuf wire byte destination") }
	*value = append([]byte(nil), data...)
	return nil
}

type headers []string
func (h *headers) String() string { return "probe metadata (values withheld)" }
func (h *headers) Set(value string) error {
	if !strings.Contains(value, "=") { return fmt.Errorf("header must have key=value form") }
	*h = append(*h, value)
	return nil
}

func run() error {
	endpoint := flag.String("endpoint", "", "explicit KFP TLS gRPC endpoint")
	caFile := flag.String("ca", "", "public trusted CA file")
	tokenFile := flag.String("token-file", "", "private short-lived token file; omit for a negative")
	namespace := flag.String("namespace", "", "explicit environment probe namespace")
	expect := flag.String("expect-code", "OK", "comma-separated expected gRPC status names")
	copies := flag.Int("authorization-copies", 1, "Authorization metadata copies (duplicate negative)")
	var extra headers
	flag.Var(&extra, "header", "additional probe metadata; repeat for duplicate/variant negatives")
	flag.Parse()
	if *endpoint == "" || *caFile == "" || *namespace == "" || *copies < 1 || *copies > 2 { return fmt.Errorf("explicit endpoint, public CA, namespace and bounded header count are required") }
	host, _, err := net.SplitHostPort(*endpoint)
	if err != nil { return err }
	ca, err := os.ReadFile(*caFile)
	if err != nil { return err }
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) { return fmt.Errorf("public CA file is invalid") }
	connection, err := grpc.NewClient(*endpoint, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})))
	if err != nil { return err }
	defer connection.Close()
	md := metadata.MD{}
	if *tokenFile != "" {
		value, err := os.ReadFile(*tokenFile)
		if err != nil { return fmt.Errorf("cannot read private token file") }
		token := strings.TrimSpace(string(value))
		if token == "" { return fmt.Errorf("empty private token file") }
		for i := 0; i < *copies; i++ { md.Append("authorization", "Bearer " + token) }
	}
	for _, row := range extra {
		key, value, _ := strings.Cut(row, "=")
		md.Append(key, value)
	}
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), md), 20*time.Second)
	defer cancel()
	// Fixed KFP e4ebca3 backend/api/v2beta1/experiment.proto:
	// ListExperimentsRequest.namespace is protobuf field 5.
	payload := protowire.AppendTag(nil, 5, protowire.BytesType)
	payload = protowire.AppendString(payload, *namespace)
	var response []byte
	err = connection.Invoke(ctx, "/kubeflow.pipelines.backend.api.v2beta1.ExperimentService/ListExperiments", payload, &response, grpc.ForceCodec(wireCodec{}))
	code := status.Code(err).String()
	hash := sha256.Sum256(response)
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"status": code, "namespace": *namespace, "responseSha256": hex.EncodeToString(hash[:]), "authorizationCopies": *copies, "extraHeaderCount": len(extra)}); err != nil { return err }
	for _, allowed := range strings.Split(*expect, ",") {
		if code == allowed { return nil }
	}
	return fmt.Errorf("gRPC status %s differs from expectation %s", code, *expect)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
