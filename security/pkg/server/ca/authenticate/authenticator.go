// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authenticate

import (
	"fmt"
	"strings"

	oidc "github.com/coreos/go-oidc"
	"golang.org/x/net/context"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"istio.io/istio/security/pkg/pki/util"
)

const (
	bearerTokenPrefix = "Bearer "
	authorizationMeta = "authorization"
	clusterIDMeta     = "clusterid"
	idTokenIssuer     = "https://accounts.google.com"

	ClientCertAuthenticatorType = "ClientCertAuthenticator"
	IDTokenAuthenticatorType    = "IDTokenAuthenticator"
)

// AuthSource represents where authentication result is derived from.
type AuthSource int

const (
	AuthSourceClientCertificate AuthSource = iota
	AuthSourceIDToken
)

// Caller carries the identity and authentication source of a caller.
type Caller struct {
	AuthSource AuthSource
	Identities []string
}

// ClientCertAuthenticator extracts identities from client certificate.
type ClientCertAuthenticator struct{}

func (cca *ClientCertAuthenticator) AuthenticatorType() string {
	return ClientCertAuthenticatorType
}

// Authenticate extracts identities from presented client certificates. This
// method assumes that certificate chain has been properly validated before
// this method is called. In other words, this method does not do certificate
// chain validation itself.
func (cca *ClientCertAuthenticator) Authenticate(ctx context.Context) (*Caller, error) {
	peer, ok := peer.FromContext(ctx)
	if !ok || peer.AuthInfo == nil {
		return nil, fmt.Errorf("no client certificate is presented")
	}

	if authType := peer.AuthInfo.AuthType(); authType != "tls" {
		return nil, fmt.Errorf("unsupported auth type: %q", authType)
	}

	tlsInfo := peer.AuthInfo.(credentials.TLSInfo)
	chains := tlsInfo.State.VerifiedChains
	if len(chains) == 0 || len(chains[0]) == 0 {
		return nil, fmt.Errorf("no verified chain is found")
	}

	ids, err := util.ExtractIDs(chains[0][0].Extensions)
	if err != nil {
		return nil, err
	}

	return &Caller{
		AuthSource: AuthSourceClientCertificate,
		Identities: ids,
	}, nil
}

// IDTokenAuthenticator extracts identity from JWT. The JWT is required to be
// transmitted using the "Bearer" authentication scheme.
type IDTokenAuthenticator struct {
	verifier *oidc.IDTokenVerifier
}

// NewIDTokenAuthenticator creates a new IDTokenAuthenticator.
func NewIDTokenAuthenticator(aud string) (*IDTokenAuthenticator, error) {
	provider, err := oidc.NewProvider(context.Background(), idTokenIssuer)
	if err != nil {
		return nil, err
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: aud})
	return &IDTokenAuthenticator{verifier}, nil
}

func (a *IDTokenAuthenticator) AuthenticatorType() string {
	return IDTokenAuthenticatorType
}

// Authenticate authenticates a caller using the JWT in the context.
func (a *IDTokenAuthenticator) Authenticate(ctx context.Context) (*Caller, error) {
	bearerToken, err := extractBearerToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("ID token extraction error: %v", err)
	}

	idToken, err := a.verifier.Verify(context.Background(), bearerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify the ID token (error %v)", err)
	}

	// for GCP-issued JWT, the service account is in the "email" field
	var sa struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&sa); err != nil {
		return nil, fmt.Errorf("failed to extract email field from ID token: %v", err)
	}

	return &Caller{
		AuthSource: AuthSourceIDToken,
		Identities: []string{sa.Email},
	}, nil
}

func extractBearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata is attached")
	}

	authHeader, exists := md[authorizationMeta]
	if !exists {
		return "", fmt.Errorf("no HTTP authorization header exists")
	}

	for _, value := range authHeader {
		if strings.HasPrefix(value, bearerTokenPrefix) {
			return strings.TrimPrefix(value, bearerTokenPrefix), nil
		}
	}

	return "", fmt.Errorf("no bearer token exists in HTTP authorization header")
}

func extractClusterID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	clusterIDHeader, exists := md[clusterIDMeta]
	if !exists {
		return ""
	}

	if len(clusterIDHeader) == 1 {
		return clusterIDHeader[0]
	}
	return ""
}
