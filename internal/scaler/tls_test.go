package scaler_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	pb "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/scaler"
)

// testCA is a throwaway certificate authority that signs the server and client
// leaves a TLS spec needs.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA() *testCA {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf returns a PEM certificate and key signed by the CA.
func (ca *testCA) leaf(serial int64, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalECPrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// startTLSServer runs an elected Server over TLS and returns its address.
// clientCA, when non-nil, is the CA client certificates must chain to.
func startTLSServer(ctx context.Context, ca *testCA, clientCA *testCA) string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	certPEM, keyPEM := ca.leaf(2, x509.ExtKeyUsageServerAuth)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	Expect(err).NotTo(HaveOccurred())
	caFile := ""
	if clientCA != nil {
		caFile = filepath.Join(dir, "client-ca.crt")
		Expect(os.WriteFile(caFile, clientCA.pem, 0o600)).To(Succeed())
	}
	cfg, err := scaler.NewTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &pair, nil }, caFile)
	Expect(err).NotTo(HaveOccurred())

	s := runtime.NewScheme()
	Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(scaledObjectFor("chat-decode", "chat-decode-deploy")).Build()
	addr := freePort()
	srv := &scaler.Server{Addr: addr, Client: c, Registry: registry.New(time.Minute), TLSConfig: cfg}
	go func() { _ = srv.Start(ctx) }()
	Eventually(func() error { return srv.Ready(nil) }, 5*time.Second).Should(Succeed())
	return addr
}

// metricSpec makes one GetMetricSpec call over a fresh connection.
func metricSpec(ctx context.Context, addr string, creds credentials.TransportCredentials) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = pb.NewExternalScalerClient(conn).GetMetricSpec(callCtx, ref("chat-decode", nil))
	return err
}

var _ = Describe("Server over TLS", func() {
	It("refuses a plaintext client and serves one that verifies the certificate", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ca := newTestCA()
		addr := startTLSServer(ctx, ca, nil)

		Expect(metricSpec(ctx, addr, insecure.NewCredentials())).To(HaveOccurred(),
			"a plaintext client reached an RPC on a TLS server")

		pool := x509.NewCertPool()
		pool.AddCert(ca.cert)
		Expect(metricSpec(ctx, addr, credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))).To(Succeed())
	})

	It("with a client CA, refuses a client without a certificate it signed", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ca := newTestCA()
		clientCA := newTestCA()
		addr := startTLSServer(ctx, ca, clientCA)
		pool := x509.NewCertPool()
		pool.AddCert(ca.cert)

		By("no client certificate")
		Expect(metricSpec(ctx, addr, credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))).To(HaveOccurred())

		By("a client certificate from the wrong CA")
		wrongPEM, wrongKey := ca.leaf(3, x509.ExtKeyUsageClientAuth)
		wrong, err := tls.X509KeyPair(wrongPEM, wrongKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(metricSpec(ctx, addr, credentials.NewTLS(&tls.Config{
			RootCAs: pool, Certificates: []tls.Certificate{wrong}, MinVersion: tls.VersionTLS12,
		}))).To(HaveOccurred())

		By("a client certificate the client CA signed")
		goodPEM, goodKey := clientCA.leaf(4, x509.ExtKeyUsageClientAuth)
		good, err := tls.X509KeyPair(goodPEM, goodKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(metricSpec(ctx, addr, credentials.NewTLS(&tls.Config{
			RootCAs: pool, Certificates: []tls.Certificate{good}, MinVersion: tls.VersionTLS12,
		}))).To(Succeed())
	})

	It("rejects a client CA file with no certificate in it", func() {
		f := filepath.Join(GinkgoT().TempDir(), "empty.crt")
		Expect(os.WriteFile(f, []byte("not a certificate"), 0o600)).To(Succeed())
		_, err := scaler.NewTLSConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil }, f)
		Expect(err).To(HaveOccurred())
	})
})
