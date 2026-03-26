# Configuring Authentication

> **Version**: 0.2.0

DecisionBox supports OIDC (OpenID Connect) authentication with any standards-compliant identity provider.
By default, authentication is disabled (NoAuth mode) for easy local development and internal deployments.
When enabled, all API requests require a valid JWT, and role-based access control (RBAC) restricts what each user can do.

## Supported Identity Providers

Any OIDC-compliant provider works.
The following have been tested:

| Provider | Token Flow | Custom Claims | Notes |
|----------|-----------|---------------|-------|
| **Auth0** | access_token | Yes (via Login Action) | Namespaced custom claims (e.g., `https://example.com/roles`) |
| **Okta** | access_token | Yes (via Authorization Server) | Custom claims on access tokens |
| **Microsoft Entra ID** | access_token | Yes (via App Roles) | Formerly Azure AD. Use `/v2.0` issuer URL. |
| **Google Workspace** | id_token | No | Opaque access tokens. Use `id_token` flow. Set `AUTH_AUDIENCE` to client ID on API. |
| **Keycloak** | access_token | Yes (via Role Mapper) | Self-hosted. Realm roles mapped to token claims. |
| **AWS Cognito** | access_token | Yes (via Pre Token Generation Lambda) | Custom claims via Lambda trigger |
| **Zitadel** | access_token | Yes (via Actions) | Open-source IdP with built-in org support |
| **Dex** | id_token | Limited | OIDC connector aggregator. Good for LDAP/SAML federation. |

## Roles and Permissions

DecisionBox uses a hierarchical role model.
A higher role inherits all permissions of lower roles.

| Action | viewer | member | admin |
|--------|--------|--------|-------|
| View projects, discoveries, insights | Yes | Yes | Yes |
| View providers, domains, pricing | Yes | Yes | Yes |
| View feedback | Yes | Yes | Yes |
| Create / update projects | | Yes | Yes |
| Trigger discoveries | | Yes | Yes |
| Submit feedback | | Yes | Yes |
| Estimate discovery costs | | Yes | Yes |
| Update prompts | | Yes | Yes |
| Delete projects | | | Yes |
| Cancel discovery runs | | | Yes |
| Delete feedback | | | Yes |
| Update pricing | | | Yes |
| Manage secrets | | | Yes |
| Test connections (warehouse, LLM) | | | Yes |

Roles are read from the JWT via the configurable `AUTH_CLAIM_ROLES` claim.
If the JWT has no roles claim, the `AUTH_DEFAULT_ROLE` is used (default: `member`).

## How It Works

The authentication flow involves three components:

1. **Dashboard** (Next.js) handles the OIDC login flow via Auth.js.
   It redirects users to the IdP, exchanges the authorization code for tokens, and stores the session.
2. **Dashboard middleware** intercepts every `/api/*` request, reads the JWT from the session, and injects it as a `Bearer` token in the `Authorization` header before proxying to the API.
3. **API** (Go) validates the JWT using the IdP's JWKS endpoint (auto-discovered via `.well-known/openid-configuration`), extracts user claims, enforces RBAC per endpoint, and scopes data access by organization ID.

```
Browser → Dashboard → IdP (login) → Dashboard (session) → API (JWT validation + RBAC)
```

## Setup: Auth0

### 1. Create an API

In the Auth0 Dashboard:

1. Go to **Applications > APIs > Create API**
2. Set:
   - **Name**: `DecisionBox API`
   - **Identifier**: `decisionbox-api` (this becomes the audience)
   - **Signing Algorithm**: RS256
3. Note the **Identifier** (e.g., `decisionbox-api`) -- you will use this as `AUTH_AUDIENCE`.

### 2. Create a Web Application

1. Go to **Applications > Applications > Create Application**
2. Select **Regular Web Application**
3. In **Settings**, note:
   - **Domain** (e.g., `your-tenant.auth0.com`)
   - **Client ID**
   - **Client Secret**
4. Set **Allowed Callback URLs**: `https://your-domain.com/api/auth/callback/oidc`
   For local development: `http://localhost:3000/api/auth/callback/oidc`
5. Set **Allowed Logout URLs**: `https://your-domain.com`
   For local development: `http://localhost:3000`

### 3. Add Custom Claims (Login Action)

Auth0 does not include roles or org_id in tokens by default.
Create a **Login Action** to add them:

1. Go to **Actions > Flows > Login**
2. Create a custom action:

```javascript
exports.onExecutePostLogin = async (event, api) => {
  const namespace = 'https://decisionbox.io';

  // Add roles to the access token
  const roles = event.authorization?.roles || [];
  api.accessToken.setCustomClaim(`${namespace}/roles`, roles);

  // Add organization ID (if using Auth0 Organizations)
  if (event.organization) {
    api.accessToken.setCustomClaim(`${namespace}/org_id`, event.organization.id);
  }
};
```

3. Deploy the action and add it to the **Login Flow**.

### 4. Create Roles and Assign Users

1. Go to **User Management > Roles**
2. Create roles: `viewer`, `member`, `admin`
3. Assign roles to users via **User Management > Users > [user] > Roles**

### 5. Set Environment Variables

**API:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://your-tenant.auth0.com/
AUTH_AUDIENCE=decisionbox-api
AUTH_CLAIM_ROLES=https://decisionbox.io/roles
AUTH_CLAIM_ORG_ID=https://decisionbox.io/org_id
```

**Dashboard:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://your-tenant.auth0.com/
AUTH_CLIENT_ID=your-client-id
AUTH_CLIENT_SECRET=your-client-secret
AUTH_AUDIENCE=decisionbox-api
AUTH_TOKEN_TYPE=access_token
AUTH_LOGOUT_URL=https://your-tenant.auth0.com/v2/logout
NEXTAUTH_URL=https://your-domain.com
NEXTAUTH_SECRET=$(openssl rand -base64 32)
```

## Setup: Google Workspace

Google issues opaque (non-JWT) access tokens.
Use the `id_token` flow instead.

### 1. Create OAuth Credentials

1. Go to [Google Cloud Console > APIs & Services > Credentials](https://console.cloud.google.com/apis/credentials)
2. Click **Create Credentials > OAuth client ID**
3. Select **Web application**
4. Set:
   - **Name**: `DecisionBox`
   - **Authorized redirect URIs**: `https://your-domain.com/api/auth/callback/oidc`
     For local development: `http://localhost:3000/api/auth/callback/oidc`
5. Note the **Client ID** and **Client Secret**

### 2. Set Environment Variables

**API:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://accounts.google.com
AUTH_AUDIENCE=your-client-id.apps.googleusercontent.com
```

Google id_tokens use standard claims (`sub`, `email`) so no claim mapping is needed.
There is no built-in roles or org_id claim.
All users get the `AUTH_DEFAULT_ROLE` (default: `member`).

**Dashboard:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://accounts.google.com
AUTH_CLIENT_ID=your-client-id.apps.googleusercontent.com
AUTH_CLIENT_SECRET=your-client-secret
AUTH_TOKEN_TYPE=id_token
NEXTAUTH_URL=https://your-domain.com
NEXTAUTH_SECRET=$(openssl rand -base64 32)
```

Note: `AUTH_AUDIENCE` is not set on the dashboard for the id_token flow.
The dashboard does not request a custom audience -- it uses the id_token directly.

## Setup: Keycloak

### 1. Create a Realm

1. In the Keycloak Admin Console, create a new realm (e.g., `decisionbox`)
2. Note the issuer URL: `https://keycloak.example.com/realms/decisionbox`

### 2. Create a Client

1. Go to **Clients > Create client**
2. Set:
   - **Client ID**: `decisionbox-dashboard`
   - **Client type**: OpenID Connect
   - **Client authentication**: On (confidential)
3. In **Settings**:
   - **Root URL**: `https://your-domain.com`
   - **Valid redirect URIs**: `https://your-domain.com/api/auth/callback/oidc`
   - **Valid post logout redirect URIs**: `https://your-domain.com`
4. In **Credentials**, copy the **Client Secret**

### 3. Create an API Resource (Audience)

1. Go to **Clients > Create client**
2. Set:
   - **Client ID**: `decisionbox-api` (this becomes the audience)
   - **Client type**: OpenID Connect
   - **Client authentication**: Off (public / resource server)

### 4. Add Role Mapper

1. Go to **Clients > decisionbox-dashboard > Client scopes > decisionbox-dashboard-dedicated**
2. Click **Add mapper > By configuration > User Realm Role**
3. Set:
   - **Name**: `roles`
   - **Token Claim Name**: `roles`
   - **Claim JSON Type**: String
   - **Add to access token**: On

### 5. Create Roles and Assign Users

1. Go to **Realm roles > Create role**
2. Create: `viewer`, `member`, `admin`
3. Assign roles to users via **Users > [user] > Role mapping**

### 6. Set Environment Variables

**API:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://keycloak.example.com/realms/decisionbox
AUTH_AUDIENCE=decisionbox-api
AUTH_CLAIM_ROLES=roles
```

**Dashboard:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://keycloak.example.com/realms/decisionbox
AUTH_CLIENT_ID=decisionbox-dashboard
AUTH_CLIENT_SECRET=your-client-secret
AUTH_AUDIENCE=decisionbox-api
AUTH_TOKEN_TYPE=access_token
AUTH_LOGOUT_URL=https://keycloak.example.com/realms/decisionbox/protocol/openid-connect/logout
NEXTAUTH_URL=https://your-domain.com
NEXTAUTH_SECRET=$(openssl rand -base64 32)
```

## Generic OIDC Setup

For any OIDC-compliant identity provider:

### 1. Identify Your IdP's OIDC Endpoints

Find the issuer URL.
It must serve a valid `.well-known/openid-configuration` document:

```bash
curl https://your-idp.example.com/.well-known/openid-configuration | jq .
```

The response must include `issuer`, `authorization_endpoint`, `token_endpoint`, and `jwks_uri`.

### 2. Create a Client Application

In your IdP, create a web application / client with:
- **Redirect URI**: `https://your-domain.com/api/auth/callback/oidc`
- **Grant type**: Authorization Code
- **Response type**: code
- Note the **Client ID** and **Client Secret**

### 3. Determine Token Flow

Check whether your IdP issues JWT access tokens:

```bash
# After obtaining an access_token, try decoding it
echo "$ACCESS_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq .
```

- If this produces valid JSON with an `aud` claim, use `AUTH_TOKEN_TYPE=access_token`.
- If the access_token is opaque (not a JWT), use `AUTH_TOKEN_TYPE=id_token` and set `AUTH_AUDIENCE` to the client ID on the API side.

### 4. Configure Claim Mapping

Map your IdP's claim names to DecisionBox's expected claims:

| DecisionBox Variable | Default Claim | Auth0 Example | Okta Example |
|---------------------|---------------|---------------|--------------|
| `AUTH_CLAIM_SUB` | `sub` | `sub` | `sub` |
| `AUTH_CLAIM_EMAIL` | `email` | `email` | `email` |
| `AUTH_CLAIM_ORG_ID` | `org_id` | `https://example.com/org_id` | `org_id` |
| `AUTH_CLAIM_ROLES` | `roles` | `https://example.com/roles` | `groups` |

### 5. Set Environment Variables

**API:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://your-idp.example.com
AUTH_AUDIENCE=your-api-identifier
AUTH_CLAIM_SUB=sub
AUTH_CLAIM_EMAIL=email
AUTH_CLAIM_ORG_ID=org_id
AUTH_CLAIM_ROLES=roles
AUTH_DEFAULT_ORG_ID=default
AUTH_DEFAULT_ROLE=member
```

**Dashboard:**

```bash
AUTH_ENABLED=true
AUTH_ISSUER_URL=https://your-idp.example.com
AUTH_CLIENT_ID=your-client-id
AUTH_CLIENT_SECRET=your-client-secret
AUTH_AUDIENCE=your-api-identifier
AUTH_TOKEN_TYPE=access_token
NEXTAUTH_URL=https://your-domain.com
NEXTAUTH_SECRET=$(openssl rand -base64 32)
```

## NoAuth Mode (Default)

When `AUTH_ENABLED` is not set or is `false`, DecisionBox runs in NoAuth mode.
All requests are authenticated as an anonymous admin user:

```go
UserPrincipal{
    Sub:   "anonymous",
    OrgID: "default",
    Roles: []string{"admin"},
}
```

This mode is for:
- Local development with `docker compose up`
- Internal deployments behind a VPN or firewall
- Evaluation and testing

To enable auth, set `AUTH_ENABLED=true` on both the API and the dashboard.

## Docker Compose with Authentication

Add auth variables to your `docker-compose.yml`:

```yaml
services:
  api:
    environment:
      - AUTH_ENABLED=true
      - AUTH_ISSUER_URL=https://your-tenant.auth0.com/
      - AUTH_AUDIENCE=decisionbox-api
      - AUTH_CLAIM_ROLES=https://decisionbox.io/roles
      - AUTH_CLAIM_ORG_ID=https://decisionbox.io/org_id

  dashboard:
    environment:
      - AUTH_ENABLED=true
      - AUTH_ISSUER_URL=https://your-tenant.auth0.com/
      - AUTH_CLIENT_ID=${AUTH_CLIENT_ID}
      - AUTH_CLIENT_SECRET=${AUTH_CLIENT_SECRET}
      - AUTH_AUDIENCE=decisionbox-api
      - AUTH_TOKEN_TYPE=access_token
      - AUTH_LOGOUT_URL=https://your-tenant.auth0.com/v2/logout
      - NEXTAUTH_URL=http://localhost:3000
      - NEXTAUTH_SECRET=${NEXTAUTH_SECRET}
```

Store secrets in a `.env` file (gitignored):

```bash
AUTH_CLIENT_ID=your-client-id
AUTH_CLIENT_SECRET=your-client-secret
NEXTAUTH_SECRET=$(openssl rand -base64 32)
```

## Helm Chart Configuration

When deploying with Helm, set auth variables in your values file:

```yaml
# decisionbox-api values
env:
  AUTH_ENABLED: "true"
  AUTH_ISSUER_URL: "https://your-tenant.auth0.com/"
  AUTH_AUDIENCE: "decisionbox-api"
  AUTH_CLAIM_ROLES: "https://decisionbox.io/roles"
  AUTH_CLAIM_ORG_ID: "https://decisionbox.io/org_id"

# decisionbox-dashboard values
env:
  AUTH_ENABLED: "true"
  AUTH_ISSUER_URL: "https://your-tenant.auth0.com/"
  AUTH_CLIENT_ID: "your-client-id"
  AUTH_AUDIENCE: "decisionbox-api"
  AUTH_TOKEN_TYPE: "access_token"
  AUTH_LOGOUT_URL: "https://your-tenant.auth0.com/v2/logout"
  NEXTAUTH_URL: "https://decisionbox.example.com"

# Secrets (use K8s secrets, not plain values)
envFromSecret:
  AUTH_CLIENT_SECRET: decisionbox-auth-secret
  NEXTAUTH_SECRET: decisionbox-auth-secret
```

## Troubleshooting

### 401 Unauthorized on all API requests

**Symptom:** The dashboard shows a login page, user logs in successfully, but all API calls return 401.

**Causes:**
- `AUTH_ENABLED=true` on the dashboard but `AUTH_ENABLED=false` (or unset) on the API. Both must match.
- `AUTH_AUDIENCE` mismatch. The audience in the JWT must match the API's `AUTH_AUDIENCE`. Check with: `echo "$TOKEN" | cut -d. -f2 | base64 -d | jq .aud`
- `AUTH_ISSUER_URL` mismatch. The issuer in the JWT must match the API's `AUTH_ISSUER_URL` exactly (including trailing slash).

### 403 Forbidden on specific endpoints

**Symptom:** User can view projects but cannot create or delete them.

**Cause:** The user's role does not have sufficient permissions.
Check the user's roles claim in the JWT.
If the JWT has no roles claim, the user gets `AUTH_DEFAULT_ROLE` (default: `member`).
Members cannot delete projects or manage secrets -- those require the `admin` role.

### "Missing authorization token" error

**Symptom:** API returns `{"error": "missing authorization token"}`.

**Cause:** The dashboard is not injecting the JWT into API requests.
Verify that `AUTH_ENABLED=true` is set on the dashboard and that the user has a valid session.
Check the dashboard logs for session errors.

### Token type mismatch (Google)

**Symptom:** Auth works with Auth0 but not with Google.

**Cause:** Google issues opaque access tokens that are not JWTs.
Set `AUTH_TOKEN_TYPE=id_token` on the dashboard.
Set `AUTH_AUDIENCE` to the Google client ID on the API (e.g., `123456.apps.googleusercontent.com`).

### Custom claims not appearing in the token

**Symptom:** Roles or org_id are not in the JWT even though they are configured in the IdP.

**Causes:**
- **Auth0:** Custom claims require a Login Action. Ensure the action is deployed and added to the Login Flow.
- **Keycloak:** Ensure the role mapper is configured with "Add to access token" enabled.
- **Okta:** Custom claims must be added to the Authorization Server's claims configuration, not just the user profile.

Verify claims are present:

```bash
# Decode the access token payload
echo "$ACCESS_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq .
```

### OIDC discovery fails on startup

**Symptom:** API fails to start with "failed to create OIDC provider".

**Cause:** The API cannot reach the `AUTH_ISSUER_URL` to fetch `.well-known/openid-configuration`.
Verify the URL is correct and the API container has network access to the IdP.

```bash
# Test from inside the container
curl -s https://your-tenant.auth0.com/.well-known/openid-configuration | jq .issuer
```

## Next Steps

- [Configuration Reference](../reference/configuration.md) -- All authentication environment variables
- [API Reference](../reference/api.md) -- Endpoint-level role requirements
- [Docker Deployment](../deployment/docker.md) -- Full deployment guide
