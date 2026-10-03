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
// adapter re-reads the real token and performs the actual expiration update with the existing name and duration.
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
	// Serial runs on process 1, which owns the in-process manager and its locks.
	Expect(suiteAccessLocks).NotTo(BeNil(), "renewal must share the running manager application locks")
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
	By("Establishing consecutive authenticated responses before measuring renewal")
	baselineCtx, cancelBaseline := context.WithTimeout(ctx, DefaultTimeout)
	baselineErr := waitForOriginBaseline(baselineCtx, func(ctx context.Context) error {
		return probeAuthenticatedOrigin(ctx, httpClient, hostname, marker, headers)
	}, 250*time.Millisecond)
	cancelBaseline()
	Expect(baselineErr).NotTo(HaveOccurred(), "establish authenticated traffic before testing renewal")
	failures := make(chan error, 1)
	done := make(chan struct{})
	var samples [3]atomic.Int32
	go func() {
		defer close(done)
		for {
			phase := delayed.phase.Load()
			err := probeAuthenticatedOrigin(probeCtx, httpClient, hostname, marker, headers)
			if err != nil {
				err = fmt.Errorf("renewal phase %d: %w", phase, err)
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
	waitSamples := func(phase int, minimum int32) {
		sampleCtx, cancel := context.WithTimeout(ctx, ShortTimeout)
		defer cancel()
		Expect(waitForContinuitySamples(sampleCtx, &samples[phase], failures, minimum)).To(Succeed(), "renewal phase %d", phase)
	}
	waitSamples(0, 2)
	result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.RequeueAfter).To(BeNumerically(">", 0))
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
	Expect(meta.IsStatusConditionTrue(policy.Status.Conditions, "Ready")).To(BeTrue(), "renewal failed: %+v", policy.Status.Conditions)
	Expect(delayed.calls.Load()).To(Equal(int32(1)), "renewal was not attempted: result=%+v conditions=%+v", result, policy.Status.Conditions)
	waitSamples(2, 4)
	cancelProbe()
	<-done
	select {
	case err := <-failures:
		Expect(err).NotTo(HaveOccurred(), "authenticated requests must remain successful throughout renewal")
	default:
	}
	Expect(samples[1].Load()).To(BeNumerically(">=", 2), "must sample while management API is delayed")
	after, err := realClient.GetServiceToken(ctx, testEnv.CloudflareAccountID, delayed.tokenID)
	Expect(err).NotTo(HaveOccurred())
	Expect(after.ExpiresAt.After(before.ExpiresAt)).To(BeTrue())
	Expect(after.ClientID).To(Equal(before.ClientID))
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
	Expect(meta.IsStatusConditionTrue(policy.Status.Conditions, "Ready")).To(BeTrue())
	GinkgoWriter.Printf("renewal continuity samples: before=%d during=%d after=%d\n", samples[0].Load(), samples[1].Load(), samples[2].Load())
}

// The baseline is a readiness precondition; no renewal occurs until it succeeds.
// Once continuity sampling starts, every request failure remains fatal.
func waitForOriginBaseline(ctx context.Context, probe func(context.Context) error, interval time.Duration) error {
	consecutive := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("authenticated baseline unavailable (last probe: %v): %w", lastErr, err)
		}
		if err := probe(ctx); err != nil {
			consecutive = 0
			lastErr = err
		} else {
			consecutive++
		}
		if consecutive >= 2 {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("authenticated baseline unavailable (last probe: %v): %w", lastErr, ctx.Err())
		case <-timer.C:
		}
	}
}

func probeAuthenticatedOrigin(ctx context.Context, httpClient *http.Client, hostname, marker string, headers http.Header) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/?renewal="+fmt.Sprint(time.Now().UnixNano()), nil)
	if err != nil {
		return err
	}
	request.Header = headers.Clone()
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("authenticated origin returned HTTP %d", response.StatusCode)
	}
	if strings.TrimSpace(string(body)) != marker {
		return fmt.Errorf("authenticated origin returned an unexpected body")
	}
	return nil
}

func waitForContinuitySamples(ctx context.Context, samples *atomic.Int32, failures <-chan error, minimum int32) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-failures:
			return err
		default:
		}
		if samples.Load() >= minimum {
			return nil
		}
		select {
		case err := <-failures:
			return err
		case <-ctx.Done():
			return fmt.Errorf("received %d of %d required continuity samples: %w", samples.Load(), minimum, ctx.Err())
		case <-ticker.C:
		}
	}
}
