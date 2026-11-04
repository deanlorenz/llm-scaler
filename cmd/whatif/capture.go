package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/whatifsource"
)

// schemeHTTPS is the scheme the bearer token and TLS are for.
const schemeHTTPS = "https"

// repeated is a flag that may be given more than once.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// captureFlags is capture's flags, as given.
type captureFlags struct {
	prometheus, tokenFile, caFile string
	insecure                      bool
	namespace                     string
	ksm                           bool
	roleOf                        repeated
	from, to                      string
	step                          time.Duration
	policy                        string
	engine, engineArgs            string
	batchCap, gpus                int
	accelerator                   string
	out                           string
}

func runCapture(ctx context.Context, args []string, out io.Writer) error {
	fs := newFlags("capture", out)
	var f captureFlags
	fs.StringVar(&f.prometheus, "prometheus", "", "the Prometheus base URL (required)")
	fs.StringVar(&f.tokenFile, "bearer-token-file", "", "a file holding a bearer token, sent to an https Prometheus")
	fs.StringVar(&f.caFile, "ca-file", "", "a PEM file of the CAs that sign Prometheus' certificate")
	fs.BoolVar(&f.insecure, "insecure-skip-verify", false, "do not verify Prometheus' certificate")
	fs.StringVar(&f.namespace, "namespace", "", "the namespace whose models to capture (required)")
	fs.BoolVar(&f.ksm, "kube-state-metrics", false, "read Pods, owners, readiness, GPUs and nodes from kube-state-metrics in the same Prometheus")
	fs.Var(&f.roleOf, "role-of", "ROLE=SELECTOR: the Pods the label selector picks serve ROLE (prefill, decode, both), as one variant; repeat per variant")
	fs.StringVar(&f.from, "from", "", "the window's start: RFC 3339 or Unix seconds (required)")
	fs.StringVar(&f.to, "to", "", "the window's end: RFC 3339 or Unix seconds (required)")
	fs.DurationVar(&f.step, "step", 15*time.Second, "the interval between cycles, taken as the run's optimize interval")
	fs.StringVar(&f.policy, "policy", "", "a scaling-policy entry (YAML), applied as the default entry; the defaults when absent")
	fs.StringVar(&f.engine, "engine", "vllm", "the inference engine the variants run: vllm or sglang")
	fs.StringVar(&f.engineArgs, "engine-args", "", "the engine's arguments, space-separated, for its parameters (kube-state-metrics has none)")
	fs.IntVar(&f.batchCap, "batch-cap", 0, "the engine's batch cap (--max-num-seqs); 0 leaves the engine's default")
	fs.IntVar(&f.gpus, "gpus-per-replica", 1, "GPUs per replica, where kube-state-metrics gives none")
	fs.StringVar(&f.accelerator, "accelerator", "", "the accelerator, where kube-state-metrics gives none")
	fs.StringVar(&f.out, "o", "", "where to write the capture, gzipped JSON lines (required)")
	if ok, err := parse(fs, args); !ok {
		return err
	}
	opts, err := f.options()
	if err != nil {
		return err
	}
	file, err := os.Create(f.out)
	if err != nil {
		return usageError{err}
	}
	sum, err := whatifsource.Capture(ctx, opts, file)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.out)
		if errors.Is(err, whatifsource.ErrNoRoleMap) {
			return usageError{err}
		}
		return fmt.Errorf("capture: %w", err)
	}
	fmt.Fprintf(out, "captured %d steps of %s from %s to %s every %s: %d cycles, %d rows, %d variants, %d range queries\n",
		sum.Steps, opts.Namespace, opts.From.Format(time.RFC3339), opts.To.Format(time.RFC3339), opts.Step,
		sum.Cycles, sum.Rows, len(sum.Variants), sum.RangeQueries)
	for _, v := range sum.Variants {
		role := v.Role
		if role == "" {
			role = "both"
		}
		fmt.Fprintf(out, "  %s/%s  %s  %d GPU %s\n", v.ModelID, v.Name, role, v.GpuCount, v.Accelerator)
	}
	for _, flag := range sum.Flags {
		fmt.Fprintf(out, "  flag: %s\n", flag)
	}
	fmt.Fprintf(out, "wrote %s\n", f.out)
	return nil
}

// options checks the flags and turns them into whatifsource.Options.
func (f captureFlags) options() (whatifsource.Options, error) {
	var opts whatifsource.Options
	switch {
	case f.prometheus == "":
		return opts, usagef("capture needs --prometheus")
	case f.namespace == "":
		return opts, usagef("capture needs --namespace")
	case f.from == "" || f.to == "":
		return opts, usagef("capture needs --from and --to")
	case f.out == "":
		return opts, usagef("capture needs -o")
	case f.step <= 0:
		return opts, usagef("--step %v is not positive", f.step)
	case !f.ksm && len(f.roleOf) == 0:
		return opts, usageError{whatifsource.ErrNoRoleMap}
	}
	from, err := parseTime(f.from)
	if err != nil {
		return opts, usagef("--from: %v", err)
	}
	to, err := parseTime(f.to)
	if err != nil {
		return opts, usagef("--to: %v", err)
	}
	if !to.After(from) {
		return opts, usagef("--to (%s) is not after --from (%s)", to.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	for _, r := range f.roleOf {
		parsed, err := whatifsource.ParseRoleOf(r)
		if err != nil {
			return opts, usageError{err}
		}
		opts.RoleOf = append(opts.RoleOf, parsed)
	}
	switch f.engine {
	case "vllm":
		opts.Engine = inferenceengine.EngineVLLM
	case "sglang":
		opts.Engine = inferenceengine.EngineSGLang
	default:
		return opts, usagef("--engine %q is not vllm or sglang", f.engine)
	}
	if f.batchCap < 0 || f.gpus < 1 {
		return opts, usagef("--batch-cap must not be negative and --gpus-per-replica must be at least 1")
	}
	if f.policy != "" {
		b, err := os.ReadFile(f.policy)
		if err != nil {
			return opts, usageError{err}
		}
		p, err := config.DecodeScalingPolicy(string(b))
		if err != nil {
			return opts, usagef("--policy %s: %v", f.policy, err)
		}
		if unknown := config.UnknownScalingPolicyKeys(string(b)); len(unknown) > 0 {
			return opts, usagef("--policy %s: keys a scaling policy does not have: %s", f.policy, strings.Join(unknown, ", "))
		}
		opts.Policy = &p
	}
	prom, err := f.client()
	if err != nil {
		return opts, err
	}
	opts.Prometheus = prom
	opts.Namespace = f.namespace
	opts.From, opts.To, opts.Step = from, to, f.step
	opts.KubeStateMetrics = f.ksm
	opts.EngineArgs = strings.Fields(f.engineArgs)
	opts.BatchCap, opts.GPUsPerReplica, opts.Accelerator = f.batchCap, f.gpus, f.accelerator
	return opts, nil
}

// parseTime reads RFC 3339 or Unix seconds, fractions allowed.
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	sec, err := strconv.ParseFloat(s, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, fmt.Errorf("%q is neither RFC 3339 nor Unix seconds", s)
	}
	return time.UnixMilli(int64(sec * 1000)).UTC(), nil
}

// client is the Prometheus API: TLS with the CA file or without
// verification, and the bearer token on https requests only, as the
// controller sends it (internal/prometheus).
func (f captureFlags) client() (promv1.API, error) {
	u, err := url.Parse(f.prometheus)
	if err != nil || (u.Scheme != "http" && u.Scheme != schemeHTTPS) || u.Host == "" {
		return nil, usagef("--prometheus %q is not an http or https URL", f.prometheus)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == schemeHTTPS {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: f.insecure} //nolint:gosec // asked for by --insecure-skip-verify
		if f.caFile != "" {
			pem, err := os.ReadFile(f.caFile)
			if err != nil {
				return nil, usageError{err}
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, usagef("--ca-file %s holds no PEM certificate", f.caFile)
			}
			tc.RootCAs = pool
		}
		transport.TLSClientConfig = tc
	}
	var rt http.RoundTripper = transport
	if f.tokenFile != "" {
		b, err := os.ReadFile(f.tokenFile)
		if err != nil {
			return nil, usageError{err}
		}
		rt = bearer{base: transport, token: strings.TrimSpace(string(b))}
	}
	c, err := api.NewClient(api.Config{Address: f.prometheus, RoundTripper: rt})
	if err != nil {
		return nil, usageError{err}
	}
	return promv1.NewAPI(c), nil
}

// bearer adds the token to https requests only, so a redirect to plain
// http does not carry it.
type bearer struct {
	base  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == schemeHTTPS {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(req)
}
