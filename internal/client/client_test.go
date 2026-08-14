package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newTestS3Client points the AWS SDK at a local stub. Retries are capped at a
// single attempt so error-path tests do not pay the SDK's default backoff.
func newTestS3Client(t *testing.T, handler http.Handler) *S3Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &S3Client{Client: s3.New(s3.Options{
		BaseEndpoint:     aws.String(srv.URL),
		Region:           "us-east-1",
		UsePathStyle:     true,
		RetryMaxAttempts: 1,
		Credentials:      credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
	})}
}

// s3Error writes an S3-style XML error document with the given status and code.
func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<Error><Code>` + code + `</Code><Message>` + code + `</Message></Error>`))
}

// The SeaweedFS S3 gateway does not return typed AWS errors reliably, so these
// classifiers are string matching. They decide whether a missing bucket is an
// error or a routine "does not exist", which makes them worth pinning down.
func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NotFound code", errors.New("api error NotFound: Not Found"), true},
		{"404 status", errors.New("https response error StatusCode: 404"), true},
		{"NoSuchBucket code", errors.New("api error NoSuchBucket: no such bucket"), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"access denied is not a not-found", errors.New("api error AccessDenied"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFoundError(tt.err); got != tt.want {
				t.Errorf("isNotFoundError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsNoSuchPolicyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NoSuchBucketPolicy", errors.New("api error NoSuchBucketPolicy"), true},
		{"NoSuchPolicy", errors.New("api error NoSuchPolicy"), true},
		{"prose variant", errors.New("The bucket policy does not exist"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoSuchPolicyError(tt.err); got != tt.want {
				t.Errorf("isNoSuchPolicyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHeadBucketExists(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))

	exists, err := c.HeadBucket(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected bucket to exist")
	}
}

// A missing bucket must be reported as absent, not as a failure — the whole
// create-if-missing path depends on it.
func TestHeadBucketMissingIsNotAnError(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
	}))

	exists, err := c.HeadBucket(context.Background(), "absent")
	if err != nil {
		t.Fatalf("a missing bucket should not be an error, got %v", err)
	}
	if exists {
		t.Error("expected bucket to be reported absent")
	}
}

func TestHeadBucketServerErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusInternalServerError, "InternalError")
	}))

	if _, err := c.HeadBucket(context.Background(), "documents"); err == nil {
		t.Fatal("expected a server error to propagate")
	}
}

func TestCreateBucket(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))

	if err := c.CreateBucket(context.Background(), "documents"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateBucketErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusInternalServerError, "InternalError")
	}))

	if err := c.CreateBucket(context.Background(), "documents"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetBucketPolicyReturnsDocument(t *testing.T) {
	const policy = `{"Version":"2012-10-17","Statement":[]}`

	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(policy))
	}))

	got, err := c.GetBucketPolicy(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != policy {
		t.Errorf("policy = %q, want %q", got, policy)
	}
}

// An unset policy is the normal first-run state and must read back as empty.
func TestGetBucketPolicyUnsetReturnsEmpty(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusNotFound, "NoSuchBucketPolicy")
	}))

	got, err := c.GetBucketPolicy(context.Background(), "documents")
	if err != nil {
		t.Fatalf("an unset policy should not be an error, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty policy, got %q", got)
	}
}

func TestGetBucketPolicyServerErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusInternalServerError, "InternalError")
	}))

	if _, err := c.GetBucketPolicy(context.Background(), "documents"); err == nil {
		t.Fatal("expected a server error to propagate")
	}
}

func TestPutBucketPolicy(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))

	if err := c.PutBucketPolicy(context.Background(), "documents", `{}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPutBucketPolicyErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusForbidden, "AccessDenied")
	}))

	if err := c.PutBucketPolicy(context.Background(), "documents", `{}`); err == nil {
		t.Fatal("expected an error")
	}
}

func TestConnectS3Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(srv.Close)

	c, err := ConnectS3(context.Background(), srv.URL, "key", "secret", "us-east-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected a client")
	}
}

// With no credentials the client must still connect, anonymously — SeaweedFS
// serves the gateway unauthenticated until an identity exists.
func TestConnectS3AnonymousWhenCredentialsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected an unsigned request, got Authorization: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(srv.Close)

	if _, err := ConnectS3(context.Background(), srv.URL, "", "", "us-east-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The retry loop must abandon a dead endpoint when the caller's context ends,
// rather than holding the full five-minute budget.
func TestConnectS3AbortsOnCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusInternalServerError, "InternalError")
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ConnectS3(ctx, srv.URL, "key", "secret", "us-east-1"); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestGetBucketVersioning(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`))
	}))

	got, err := c.GetBucketVersioning(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Enabled" {
		t.Errorf("status = %q, want Enabled", got)
	}
}

// A bucket that never had versioning configured reports an empty status rather
// than failing, which is what lets the reconciler treat it as "not enabled".
func TestGetBucketVersioningUnsetReturnsEmpty(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<VersioningConfiguration></VersioningConfiguration>`))
	}))

	got, err := c.GetBucketVersioning(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("status = %q, want empty", got)
	}
}

func TestPutBucketVersioning(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{"enable", true, "Enabled"},
		{"suspend", false, "Suspended"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				body = string(b)
				w.WriteHeader(http.StatusOK)
			}))

			if err := c.PutBucketVersioning(context.Background(), "documents", tt.enabled); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(body, tt.want) {
				t.Errorf("request body %q should contain %q", body, tt.want)
			}
		})
	}
}

func TestGetBucketLifecycle(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<LifecycleConfiguration><Rule>` +
			`<ID>expire-30d</ID><Status>Enabled</Status>` +
			`<Filter><Prefix>logs/</Prefix></Filter>` +
			`<Expiration><Days>30</Days></Expiration>` +
			`</Rule></LifecycleConfiguration>`))
	}))

	rules, err := c.GetBucketLifecycle(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ID != "expire-30d" || rules[0].ExpirationDays != 30 {
		t.Errorf("rule decoded wrong: %+v", rules[0])
	}
	if rules[0].Prefix != "logs/" {
		t.Errorf("prefix = %q, want logs/", rules[0].Prefix)
	}
	if !rules[0].Enabled {
		t.Error("rule should be enabled")
	}
}

// SeaweedFS may return the deprecated top-level Prefix instead of a Filter.
func TestGetBucketLifecycleReadsLegacyPrefix(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<LifecycleConfiguration><Rule>` +
			`<ID>legacy</ID><Status>Enabled</Status><Prefix>old/</Prefix>` +
			`<Expiration><Days>7</Days></Expiration>` +
			`</Rule></LifecycleConfiguration>`))
	}))

	rules, err := c.GetBucketLifecycle(context.Background(), "documents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 || rules[0].Prefix != "old/" {
		t.Errorf("legacy prefix not read: %+v", rules)
	}
}

// A bucket with no lifecycle config is the normal first-run state.
func TestGetBucketLifecycleUnsetReturnsNoRules(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusNotFound, "NoSuchLifecycleConfiguration")
	}))

	rules, err := c.GetBucketLifecycle(context.Background(), "documents")
	if err != nil {
		t.Fatalf("an unset lifecycle should not be an error, got %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected no rules, got %d", len(rules))
	}
}

func TestGetBucketLifecycleServerErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusForbidden, "AccessDenied")
	}))

	if _, err := c.GetBucketLifecycle(context.Background(), "documents"); err == nil {
		t.Fatal("expected a server error to propagate")
	}
}

func TestPutBucketLifecycle(t *testing.T) {
	var body string
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))

	rules := []LifecycleRule{
		{ID: "expire-30d", Prefix: "logs/", ExpirationDays: 30, Enabled: true},
		{ID: "off", Prefix: "", ExpirationDays: 7, Enabled: false},
	}
	if err := c.PutBucketLifecycle(context.Background(), "documents", rules); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"expire-30d", "logs/", "<Days>30</Days>", "Enabled", "Disabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("request body should contain %q, got: %s", want, body)
		}
	}
}

func TestPutBucketLifecycleErrorPropagates(t *testing.T) {
	c := newTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusInternalServerError, "InternalError")
	}))

	err := c.PutBucketLifecycle(context.Background(), "documents", []LifecycleRule{{ID: "x", ExpirationDays: 1}})
	if err == nil {
		t.Fatal("expected an error")
	}
}
