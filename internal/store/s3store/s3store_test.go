package s3store_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/store/s3store"
	"github.com/eyelock/ynf/internal/store/storetest"
)

// minio starts MinIO in docker for the conformance suite, or uses YNF_S3_ENDPOINT. Without either
// the S3 tests are skipped: the suite is the same one SQLite passes.
func minio(t *testing.T) string {
	t.Helper()
	if ep := os.Getenv("YNF_S3_ENDPOINT"); ep != "" {
		return ep
	}
	if os.Getenv("YNF_DOCKER_TESTS") == "" {
		t.Skip("set YNF_DOCKER_TESTS=1 (MinIO in docker) or YNF_S3_ENDPOINT")
	}
	name := fmt.Sprintf("ynf-minio-test-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::9000",
		"-e", "MINIO_ROOT_USER=ynf", "-e", "MINIO_ROOT_PASSWORD=ynf-test-secret",
		"cgr.dev/chainguard/minio:latest", "server", "/data").CombinedOutput()
	if err != nil {
		t.Fatalf("start MinIO: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	port, err := exec.Command("docker", "port", name, "9000").Output()
	if err != nil {
		t.Fatal(err)
	}
	ep := "http://" + strings.TrimSpace(strings.Split(string(port), "\n")[0])
	for range 120 {
		if r, err := http.Get(ep + "/minio/health/live"); err == nil && r.StatusCode == 200 {
			_ = r.Body.Close()
			return ep
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("MinIO did not start")
	return ""
}

var buckets atomic.Int64

func TestConformance(t *testing.T) {
	ep := minio(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "ynf")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ynf-test-secret")
	t.Setenv("AWS_REGION", "us-east-1")
	storetest.Run(t, func(t *testing.T) store.Store {
		bucket := fmt.Sprintf("ynf-test-%d-%d", os.Getpid(), buckets.Add(1))
		s, err := s3store.Open(context.Background(), "s3://"+bucket+"/state?region=us-east-1&path_style=true&endpoint="+ep)
		if err != nil {
			t.Fatal(err)
		}
		c := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(ep), UsePathStyle: true,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "ynf", SecretAccessKey: "ynf-test-secret"}, nil
			})})
		if _, err := c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestOpenRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"sqlite://x", "s3://", "::"} {
		if _, err := s3store.Open(context.Background(), u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
