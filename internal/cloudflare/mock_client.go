package cloudflare

import "context"

// Compile-time interface check.
var _ Client = (*MockClient)(nil)

// MockClient is a test double for Client that delegates each method to a
// configurable function field. Unstubbed methods return zero values.
//
// Tests set only the fields they exercise:
//
//	mock := NewMockClient()
//	mock.GetTunnelFunc = func(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
//	    return &Tunnel{ID: "t1", Name: "test"}, nil
//	}
//	svc := NewTunnelService(mock)
type MockClient struct {
	// Tunnel operations
	GetTunnelFunc                 func(ctx context.Context, accountID, tunnelID string) (*Tunnel, error)
	GetTunnelByNameFunc           func(ctx context.Context, accountID, name string) (*Tunnel, error)
	CreateTunnelFunc              func(ctx context.Context, accountID string, params CreateTunnelParams) (*Tunnel, error)
	DeleteTunnelFunc              func(ctx context.Context, accountID, tunnelID string) error
	DeleteTunnelConnectionsFunc   func(ctx context.Context, accountID, tunnelID string) error
	GetTunnelTokenFunc            func(ctx context.Context, accountID, tunnelID string) (string, error)
	UpdateTunnelConfigurationFunc func(ctx context.Context, accountID, tunnelID string, config TunnelConfiguration) error

	// DNS operations
	ListDNSRecordsFunc           func(ctx context.Context, zoneID string) ([]DNSRecord, error)
	ListDNSRecordsByNameTypeFunc func(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error)
	CreateDNSRecordFunc          func(ctx context.Context, zoneID string, record DNSRecord) (*DNSRecord, error)
	UpdateDNSRecordFunc          func(ctx context.Context, zoneID, recordID string, record DNSRecord) (*DNSRecord, error)
	DeleteDNSRecordFunc          func(ctx context.Context, zoneID, recordID string) error

	// Zone operations
	ListZonesFunc     func(ctx context.Context) ([]Zone, error)
	GetZoneByNameFunc func(ctx context.Context, name string) (*Zone, error)

	// Token validation
	ValidateTokenFunc func(ctx context.Context, accountID string) error

	// Account operations
	ListAccountsFunc     func(ctx context.Context) ([]Account, error)
	GetAccountByNameFunc func(ctx context.Context, name string) (*Account, error)

	// Access Application operations
	CreateAccessApplicationFunc    func(ctx context.Context, accountID string, params ApplicationParams) (*AccessApplication, error)
	GetAccessApplicationFunc       func(ctx context.Context, accountID, appID string) (*AccessApplication, error)
	UpdateAccessApplicationFunc    func(ctx context.Context, accountID, appID string, params ApplicationParams) (*AccessApplication, error)
	DeleteAccessApplicationFunc    func(ctx context.Context, accountID, appID string) error
	ListAccessApplicationsFunc     func(ctx context.Context, accountID string) ([]AccessApplication, error)
	GetAccessApplicationByNameFunc func(ctx context.Context, accountID, name string) (*AccessApplication, error)

	// Access Policy operations
	CreateAccessPolicyFunc func(ctx context.Context, accountID, appID string, params PolicyParams) (*AccessPolicy, error)
	GetAccessPolicyFunc    func(ctx context.Context, accountID, appID, policyID string) (*AccessPolicy, error)
	UpdateAccessPolicyFunc func(ctx context.Context, accountID, appID, policyID string, params PolicyParams) (*AccessPolicy, error)
	DeleteAccessPolicyFunc func(ctx context.Context, accountID, appID, policyID string) error
	ListAccessPoliciesFunc func(ctx context.Context, accountID, appID string) ([]AccessPolicy, error)

	// Access Group operations
	CreateAccessGroupFunc    func(ctx context.Context, accountID string, params GroupParams) (*AccessGroup, error)
	GetAccessGroupFunc       func(ctx context.Context, accountID, groupID string) (*AccessGroup, error)
	UpdateAccessGroupFunc    func(ctx context.Context, accountID, groupID string, params GroupParams) (*AccessGroup, error)
	DeleteAccessGroupFunc    func(ctx context.Context, accountID, groupID string) error
	ListAccessGroupsFunc     func(ctx context.Context, accountID string) ([]AccessGroup, error)
	GetAccessGroupByNameFunc func(ctx context.Context, accountID, name string) (*AccessGroup, error)

	// Service Token operations
	CreateServiceTokenFunc  func(ctx context.Context, accountID string, params ServiceTokenParams) (*ServiceTokenWithSecret, error)
	GetServiceTokenFunc     func(ctx context.Context, accountID, tokenID string) (*ServiceToken, error)
	UpdateServiceTokenFunc  func(ctx context.Context, accountID, tokenID string, params ServiceTokenParams) (*ServiceToken, error)
	DeleteServiceTokenFunc  func(ctx context.Context, accountID, tokenID string) error
	ListServiceTokensFunc   func(ctx context.Context, accountID string) ([]ServiceToken, error)
	RotateServiceTokenFunc  func(ctx context.Context, accountID, tokenID string) (*ServiceTokenWithSecret, error)
	RefreshServiceTokenFunc func(ctx context.Context, accountID, tokenID string) (*ServiceToken, error)
}

// NewMockClient returns a MockClient with all function fields nil.
// Unstubbed methods return zero values without panicking.
func NewMockClient() *MockClient {
	return &MockClient{}
}

// Tunnel operations

func (m *MockClient) GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
	if m.GetTunnelFunc != nil {
		return m.GetTunnelFunc(ctx, accountID, tunnelID)
	}
	return nil, nil
}

func (m *MockClient) GetTunnelByName(ctx context.Context, accountID, name string) (*Tunnel, error) {
	if m.GetTunnelByNameFunc != nil {
		return m.GetTunnelByNameFunc(ctx, accountID, name)
	}
	return nil, nil
}

func (m *MockClient) CreateTunnel(ctx context.Context, accountID string, params CreateTunnelParams) (*Tunnel, error) {
	if m.CreateTunnelFunc != nil {
		return m.CreateTunnelFunc(ctx, accountID, params)
	}
	return nil, nil
}

func (m *MockClient) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	if m.DeleteTunnelFunc != nil {
		return m.DeleteTunnelFunc(ctx, accountID, tunnelID)
	}
	return nil
}

func (m *MockClient) DeleteTunnelConnections(ctx context.Context, accountID, tunnelID string) error {
	if m.DeleteTunnelConnectionsFunc != nil {
		return m.DeleteTunnelConnectionsFunc(ctx, accountID, tunnelID)
	}
	return nil
}

func (m *MockClient) GetTunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	if m.GetTunnelTokenFunc != nil {
		return m.GetTunnelTokenFunc(ctx, accountID, tunnelID)
	}
	return "", nil
}

func (m *MockClient) UpdateTunnelConfiguration(ctx context.Context, accountID, tunnelID string, config TunnelConfiguration) error {
	if m.UpdateTunnelConfigurationFunc != nil {
		return m.UpdateTunnelConfigurationFunc(ctx, accountID, tunnelID, config)
	}
	return nil
}

// DNS operations

func (m *MockClient) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	if m.ListDNSRecordsFunc != nil {
		return m.ListDNSRecordsFunc(ctx, zoneID)
	}
	return nil, nil
}

func (m *MockClient) ListDNSRecordsByNameType(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error) {
	if m.ListDNSRecordsByNameTypeFunc != nil {
		return m.ListDNSRecordsByNameTypeFunc(ctx, zoneID, name, recordType)
	}
	return nil, nil
}

func (m *MockClient) CreateDNSRecord(ctx context.Context, zoneID string, record DNSRecord) (*DNSRecord, error) {
	if m.CreateDNSRecordFunc != nil {
		return m.CreateDNSRecordFunc(ctx, zoneID, record)
	}
	return nil, nil
}

func (m *MockClient) UpdateDNSRecord(ctx context.Context, zoneID, recordID string, record DNSRecord) (*DNSRecord, error) {
	if m.UpdateDNSRecordFunc != nil {
		return m.UpdateDNSRecordFunc(ctx, zoneID, recordID, record)
	}
	return nil, nil
}

func (m *MockClient) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	if m.DeleteDNSRecordFunc != nil {
		return m.DeleteDNSRecordFunc(ctx, zoneID, recordID)
	}
	return nil
}

// Zone operations

func (m *MockClient) ListZones(ctx context.Context) ([]Zone, error) {
	if m.ListZonesFunc != nil {
		return m.ListZonesFunc(ctx)
	}
	return nil, nil
}

func (m *MockClient) GetZoneByName(ctx context.Context, name string) (*Zone, error) {
	if m.GetZoneByNameFunc != nil {
		return m.GetZoneByNameFunc(ctx, name)
	}
	return nil, nil
}

// Token validation

func (m *MockClient) ValidateToken(ctx context.Context, accountID string) error {
	if m.ValidateTokenFunc != nil {
		return m.ValidateTokenFunc(ctx, accountID)
	}
	return nil
}

// Account operations

func (m *MockClient) ListAccounts(ctx context.Context) ([]Account, error) {
	if m.ListAccountsFunc != nil {
		return m.ListAccountsFunc(ctx)
	}
	return nil, nil
}

func (m *MockClient) GetAccountByName(ctx context.Context, name string) (*Account, error) {
	if m.GetAccountByNameFunc != nil {
		return m.GetAccountByNameFunc(ctx, name)
	}
	return nil, nil
}

// Access Application operations

func (m *MockClient) CreateAccessApplication(ctx context.Context, accountID string, params ApplicationParams) (*AccessApplication, error) {
	if m.CreateAccessApplicationFunc != nil {
		return m.CreateAccessApplicationFunc(ctx, accountID, params)
	}
	return nil, nil
}

func (m *MockClient) GetAccessApplication(ctx context.Context, accountID, appID string) (*AccessApplication, error) {
	if m.GetAccessApplicationFunc != nil {
		return m.GetAccessApplicationFunc(ctx, accountID, appID)
	}
	return nil, nil
}

func (m *MockClient) UpdateAccessApplication(ctx context.Context, accountID, appID string, params ApplicationParams) (*AccessApplication, error) {
	if m.UpdateAccessApplicationFunc != nil {
		return m.UpdateAccessApplicationFunc(ctx, accountID, appID, params)
	}
	return nil, nil
}

func (m *MockClient) DeleteAccessApplication(ctx context.Context, accountID, appID string) error {
	if m.DeleteAccessApplicationFunc != nil {
		return m.DeleteAccessApplicationFunc(ctx, accountID, appID)
	}
	return nil
}

func (m *MockClient) ListAccessApplications(ctx context.Context, accountID string) ([]AccessApplication, error) {
	if m.ListAccessApplicationsFunc != nil {
		return m.ListAccessApplicationsFunc(ctx, accountID)
	}
	return nil, nil
}

func (m *MockClient) GetAccessApplicationByName(ctx context.Context, accountID, name string) (*AccessApplication, error) {
	if m.GetAccessApplicationByNameFunc != nil {
		return m.GetAccessApplicationByNameFunc(ctx, accountID, name)
	}
	return nil, nil
}

// Access Policy operations

func (m *MockClient) CreateAccessPolicy(ctx context.Context, accountID, appID string, params PolicyParams) (*AccessPolicy, error) {
	if m.CreateAccessPolicyFunc != nil {
		return m.CreateAccessPolicyFunc(ctx, accountID, appID, params)
	}
	return nil, nil
}

func (m *MockClient) GetAccessPolicy(ctx context.Context, accountID, appID, policyID string) (*AccessPolicy, error) {
	if m.GetAccessPolicyFunc != nil {
		return m.GetAccessPolicyFunc(ctx, accountID, appID, policyID)
	}
	return nil, nil
}

func (m *MockClient) UpdateAccessPolicy(ctx context.Context, accountID, appID, policyID string, params PolicyParams) (*AccessPolicy, error) {
	if m.UpdateAccessPolicyFunc != nil {
		return m.UpdateAccessPolicyFunc(ctx, accountID, appID, policyID, params)
	}
	return nil, nil
}

func (m *MockClient) DeleteAccessPolicy(ctx context.Context, accountID, appID, policyID string) error {
	if m.DeleteAccessPolicyFunc != nil {
		return m.DeleteAccessPolicyFunc(ctx, accountID, appID, policyID)
	}
	return nil
}

func (m *MockClient) ListAccessPolicies(ctx context.Context, accountID, appID string) ([]AccessPolicy, error) {
	if m.ListAccessPoliciesFunc != nil {
		return m.ListAccessPoliciesFunc(ctx, accountID, appID)
	}
	return nil, nil
}

// Access Group operations

func (m *MockClient) CreateAccessGroup(ctx context.Context, accountID string, params GroupParams) (*AccessGroup, error) {
	if m.CreateAccessGroupFunc != nil {
		return m.CreateAccessGroupFunc(ctx, accountID, params)
	}
	return nil, nil
}

func (m *MockClient) GetAccessGroup(ctx context.Context, accountID, groupID string) (*AccessGroup, error) {
	if m.GetAccessGroupFunc != nil {
		return m.GetAccessGroupFunc(ctx, accountID, groupID)
	}
	return nil, nil
}

func (m *MockClient) UpdateAccessGroup(ctx context.Context, accountID, groupID string, params GroupParams) (*AccessGroup, error) {
	if m.UpdateAccessGroupFunc != nil {
		return m.UpdateAccessGroupFunc(ctx, accountID, groupID, params)
	}
	return nil, nil
}

func (m *MockClient) DeleteAccessGroup(ctx context.Context, accountID, groupID string) error {
	if m.DeleteAccessGroupFunc != nil {
		return m.DeleteAccessGroupFunc(ctx, accountID, groupID)
	}
	return nil
}

func (m *MockClient) ListAccessGroups(ctx context.Context, accountID string) ([]AccessGroup, error) {
	if m.ListAccessGroupsFunc != nil {
		return m.ListAccessGroupsFunc(ctx, accountID)
	}
	return nil, nil
}

func (m *MockClient) GetAccessGroupByName(ctx context.Context, accountID, name string) (*AccessGroup, error) {
	if m.GetAccessGroupByNameFunc != nil {
		return m.GetAccessGroupByNameFunc(ctx, accountID, name)
	}
	return nil, nil
}

// Service Token operations

func (m *MockClient) CreateServiceToken(ctx context.Context, accountID string, params ServiceTokenParams) (*ServiceTokenWithSecret, error) {
	if m.CreateServiceTokenFunc != nil {
		return m.CreateServiceTokenFunc(ctx, accountID, params)
	}
	return nil, nil
}

func (m *MockClient) GetServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceToken, error) {
	if m.GetServiceTokenFunc != nil {
		return m.GetServiceTokenFunc(ctx, accountID, tokenID)
	}
	return nil, nil
}

func (m *MockClient) UpdateServiceToken(ctx context.Context, accountID, tokenID string, params ServiceTokenParams) (*ServiceToken, error) {
	if m.UpdateServiceTokenFunc != nil {
		return m.UpdateServiceTokenFunc(ctx, accountID, tokenID, params)
	}
	return nil, nil
}

func (m *MockClient) DeleteServiceToken(ctx context.Context, accountID, tokenID string) error {
	if m.DeleteServiceTokenFunc != nil {
		return m.DeleteServiceTokenFunc(ctx, accountID, tokenID)
	}
	return nil
}

func (m *MockClient) ListServiceTokens(ctx context.Context, accountID string) ([]ServiceToken, error) {
	if m.ListServiceTokensFunc != nil {
		return m.ListServiceTokensFunc(ctx, accountID)
	}
	return nil, nil
}

func (m *MockClient) RotateServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceTokenWithSecret, error) {
	if m.RotateServiceTokenFunc != nil {
		return m.RotateServiceTokenFunc(ctx, accountID, tokenID)
	}
	return nil, nil
}

func (m *MockClient) RefreshServiceToken(ctx context.Context, accountID, tokenID string) (*ServiceToken, error) {
	if m.RefreshServiceTokenFunc != nil {
		return m.RefreshServiceTokenFunc(ctx, accountID, tokenID)
	}
	return nil, nil
}
