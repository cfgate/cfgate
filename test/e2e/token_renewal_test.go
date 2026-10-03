package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Only the inventory's expiration is advanced into the renewal window. The
// adapter re-reads the real token and performs the actual duration-only update.
// This avoids waiting for a minimum one-hour token to age during every suite.
type renewalDueClient struct {
	cloudflare.Client
	tokenID string
	phase   atomic.Int32
	calls   atomic.Int32
}

func (c *renewalDueClient) ListServiceTokens(ctx context.Context, account string) ([]cloudflare.ServiceToken, error) {
	tokens, err := c.Client.ListServiceTokens(ctx, account)
	for i := range tokens {
		if tokens[i].ID == c.tokenID {
			tokens[i].ExpiresAt = time.Now().Add(time.Minute)
		}
	}
	return tokens, err
}
func (c *renewalDueClient) ExtendServiceTokenExpiration(ctx context.Context, account, id string, expected cloudflare.ServiceToken) (*cloudflare.ServiceToken, error) {
	c.calls.Add(1)
	c.phase.Store(1)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	result, err := c.Client.ExtendServiceTokenExpiration(ctx, account, id, expected)
	c.phase.Store(2)
	return result, err
}

func verifyTokenRenewalContinuity(ctx SpecContext, policy *cfgatev1alpha1.CloudflareAccessPolicy, tokenName, hostname, marker string, headers http.Header) {
	realClient, err := cloudflare.NewClient(testEnv.CloudflareAPIToken)
	Expect(err).NotTo(HaveOccurred())
	delayed := &renewalDueClient{Client: realClient, tokenID: policy.Status.ServiceTokenIDs[tokenName]}
	before, err := realClient.GetServiceToken(ctx, testEnv.CloudflareAccountID, delayed.tokenID)
	Expect(err).NotTo(HaveOccurred())
	reconciler := &controller.CloudflareAccessPolicyReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: scheme, CFClient: delayed, AccessLocks: suiteAccessLocks, InstallationNamespace: e2eSystemNamespace()}
	httpClient := newMaintenanceHTTPClient()
	defer httpClient.CloseIdleConnections()
	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()
	failures := make(chan error, 1)
	done := make(chan struct{})
	var samples [3]atomic.Int32
	go func() {
		defer close(done)
		for {
			phase := delayed.phase.Load()
			request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "https://"+hostname+"/?renewal="+fmt.Sprint(time.Now().UnixNano()), nil)
			if err == nil {
				request.Header = headers.Clone()
				var response *http.Response
				response, err = httpClient.Do(request)
				if err == nil {
					var body []byte
					body, err = io.ReadAll(io.LimitReader(response.Body, 4096))
					_ = response.Body.Close()
					if err == nil && (response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != marker) {
						err = fmt.Errorf("renewal phase %d: status=%d, expected authenticated origin response", phase, response.StatusCode)
					}
				}
			}
			if err != nil {
				if probeCtx.Err() == nil {
					failures <- err
				}
				return
			}
			samples[phase].Add(1)
			select {
			case <-probeCtx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	defer func() { cancelProbe(); <-done }()
	Eventually(ctx, func() int32 { return samples[0].Load() }, ShortTimeout, DefaultInterval).Should(BeNumerically(">=", 2))
	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.RequeueAfter).To(BeNumerically(">", 0))
	Expect(delayed.calls.Load()).To(Equal(int32(1)))
	Eventually(ctx, func() int32 { return samples[2].Load() }, ShortTimeout, DefaultInterval).Should(BeNumerically(">=", 4))
	cancelProbe()
	<-done
	Expect(failures).NotTo(Receive(), "authenticated requests must remain successful throughout renewal")
	Expect(samples[1].Load()).To(BeNumerically(">=", 2), "must sample while management API is delayed")
	after, err := realClient.GetServiceToken(ctx, testEnv.CloudflareAccountID, delayed.tokenID)
	Expect(err).NotTo(HaveOccurred())
	Expect(after.ExpiresAt.After(before.ExpiresAt)).To(BeTrue())
	Expect(after.ClientID).To(Equal(before.ClientID))
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
	Expect(meta.IsStatusConditionTrue(policy.Status.Conditions, "Ready")).To(BeTrue())
	GinkgoWriter.Printf("renewal continuity samples: before=%d during=%d after=%d\n", samples[0].Load(), samples[1].Load(), samples[2].Load())
}
