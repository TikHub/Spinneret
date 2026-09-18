import { request, type APIRequestContext } from '@playwright/test';

import { ADMIN_PASSWORD, ADMIN_USER, BASE_URL, NAMESPACE } from './env';

/**
 * Minimal Connect (JSON) client used for test setup and cleanup. The console
 * itself is always driven through the UI; this client only prepares fixtures
 * that would be tedious or slow to create by clicking.
 *
 * The server serialises responses with proto field names (snake_case), while it
 * accepts both spellings on input, so requests here use snake_case too.
 */
export class Api {
  readonly tenantId: string;
  readonly userId: string;
  private readonly ctx: APIRequestContext;

  private constructor(ctx: APIRequestContext, tenantId: string, userId: string) {
    this.ctx = ctx;
    this.tenantId = tenantId;
    this.userId = userId;
  }

  /** Signs in with the console administrator and returns a ready client. */
  static async login(): Promise<Api> {
    const ctx = await request.newContext({ baseURL: BASE_URL });
    const res = await ctx.post('/spinneret.v1.AuthService/Login', {
      headers: { 'content-type': 'application/json', 'x-spinneret-csrf': '1' },
      data: { username: ADMIN_USER, password: ADMIN_PASSWORD },
    });
    if (!res.ok()) throw new Error(`login failed: ${res.status()} ${await res.text()}`);
    const body = (await res.json()) as {
      user: { id: string };
      tenants: { tenant: { id: string; name: string } }[];
    };
    const tenant = body.tenants.find((t) => t.tenant.name === 'default') ?? body.tenants[0];
    if (!tenant) throw new Error('the console administrator has no tenant access');
    return new Api(ctx, tenant.tenant.id, body.user.id);
  }

  async dispose(): Promise<void> {
    await this.ctx.dispose();
  }

  /** Calls one Connect RPC and returns the decoded response. */
  async call<T = Record<string, unknown>>(method: string, body: unknown = {}): Promise<T> {
    const res = await this.ctx.post(`/spinneret.v1.${method}`, {
      headers: {
        'content-type': 'application/json',
        'x-spinneret-csrf': '1',
        'x-spinneret-tenant': this.tenantId,
      },
      data: body as Record<string, unknown>,
    });
    const text = await res.text();
    if (!res.ok()) throw new Error(`${method} failed: ${res.status()} ${text}`);
    return (text ? JSON.parse(text) : {}) as T;
  }

  /** Calls an RPC and returns undefined instead of throwing on a server error. */
  async tryCall<T = Record<string, unknown>>(method: string, body: unknown = {}): Promise<T | undefined> {
    try {
      return await this.call<T>(method, body);
    } catch {
      return undefined;
    }
  }

  // --- sites -------------------------------------------------------------

  async createSite(name: string, opts: { displayName?: string; clients?: string[] } = {}): Promise<string> {
    const res = await this.call<{ site: { id: string } }>('SiteAdminService/CreateSite', {
      namespace: NAMESPACE,
      name,
      display_name: opts.displayName ?? name,
      clients: opts.clients ?? ['web'],
    });
    return res.site.id;
  }

  /** Deletes a site and everything below it; missing sites are ignored. */
  async deleteSite(name: string): Promise<void> {
    const site = await this.tryCall<{ site?: { id: string } }>('SiteAdminService/GetSite', {
      namespace: NAMESPACE,
      name,
    });
    const id = site?.site?.id;
    if (!id) return;
    await this.tryCall('SiteAdminService/DeleteSite', { id, force: true });
  }

  async createEndpointGroup(site: string, name: string, client = 'web'): Promise<void> {
    await this.call('SiteAdminService/CreateEndpointGroup', {
      namespace: NAMESPACE,
      site,
      client,
      name,
      display_name: name,
    });
  }

  // --- identities --------------------------------------------------------

  /** Creates an identity type from its YAML spec and returns its name. */
  async createIdentityType(site: string, specYaml: string): Promise<string> {
    const res = await this.call<{ identity_type: { name: string } }>(
      'IdentityAdminService/CreateIdentityType',
      { namespace: NAMESPACE, site, spec_yaml: specYaml },
    );
    return res.identity_type.name;
  }

  /** Imports JSONL identities of an existing type; returns the number of created rows. */
  async importIdentities(site: string, type: string, rows: Record<string, unknown>[]): Promise<number> {
    const res = await this.call<{ created?: number }>('IdentityAdminService/ImportIdentities', {
      namespace: NAMESPACE,
      site,
      type,
      format: 'jsonl',
      data: rows.map((row) => JSON.stringify(row)).join('\n'),
    });
    return Number(res.created ?? 0);
  }

  // --- node API ----------------------------------------------------------

  /** Creates an API token for the node services and returns its plaintext. */
  async createNodeToken(name: string): Promise<string> {
    const res = await this.call<{ plaintext: string }>('AccessAdminService/CreateToken', {
      namespace: NAMESPACE,
      name,
      scopes: ['lease:acquire', 'report:write'],
    });
    return res.plaintext;
  }

  async revokeTokenByName(name: string): Promise<void> {
    const list = await this.tryCall<{ tokens?: { id: string; name: string; revoked_at?: string }[] }>(
      'AccessAdminService/ListTokens',
      { namespace: NAMESPACE, page_size: 500 },
    );
    const token = list?.tokens?.find((t) => t.name === name && !t.revoked_at);
    if (token) await this.tryCall('AccessAdminService/RevokeToken', { id: token.id });
  }

  /** Calls a node RPC with a node token instead of the console session. */
  async nodeCall<T = Record<string, unknown>>(token: string, method: string, body: unknown): Promise<T> {
    const res = await this.ctx.post(`/spinneret.v1.${method}`, {
      headers: { 'content-type': 'application/json', authorization: `Bearer ${token}` },
      data: body as Record<string, unknown>,
    });
    const text = await res.text();
    if (!res.ok()) throw new Error(`${method} failed: ${res.status()} ${text}`);
    return (text ? JSON.parse(text) : {}) as T;
  }

  /**
   * Acquires one lease and reports it with the given HTTP status, so the
   * dashboards have a request event (and, for a failing status, a risk event).
   * Acquiring is retried: an identity that was just used is inside its reuse
   * interval and the scheduler refuses it.
   */
  async acquireAndReport(
    token: string,
    site: string,
    opts: { client?: string; endpointGroup?: string; httpStatus?: number; uri?: string } = {},
  ): Promise<void> {
    let lease: { lease: { lease_id: string } } | undefined;
    let lastError: unknown;
    for (let attempt = 0; attempt < 5 && !lease; attempt += 1) {
      try {
        lease = await this.nodeCall<{ lease: { lease_id: string } }>(token, 'LeaseService/Acquire', {
          site,
          client: opts.client ?? 'web',
          endpoint_group: opts.endpointGroup ?? '_default',
          wait_ms: 2000,
        });
      } catch (err) {
        lastError = err;
        await new Promise((resolve) => setTimeout(resolve, 1500));
      }
    }
    if (!lease) throw new Error(`could not acquire a lease on ${site}: ${String(lastError)}`);

    const now = new Date();
    const res = await this.nodeCall<{ accepted?: number; rejected?: { message: string }[] }>(
      token,
      'ReportService/Report',
      {
        reports: [
          {
            report_id: `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
            lease_id: lease.lease.lease_id,
            uri: opts.uri ?? '/api/search?q=e2e',
            method: 'GET',
            http_status: opts.httpStatus ?? 200,
            latency_ms: 12,
            response_bytes: 512,
            started_at: new Date(now.getTime() - 12).toISOString(),
            finished_at: now.toISOString(),
            release: true,
          },
        ],
      },
    );
    if (!res.accepted) {
      throw new Error(`report rejected: ${JSON.stringify(res.rejected ?? [])}`);
    }
  }

  /**
   * Runs `count` acquire/report cycles against a site so the dashboards have
   * fresh traffic. Every fifth request is reported as rate limited, which gives
   * the charts a non-zero risk ratio.
   */
  async seedTraffic(token: string, site: string, count: number): Promise<number> {
    let done = 0;
    for (let i = 0; i < count; i += 1) {
      try {
        await this.acquireAndReport(token, site, { httpStatus: i % 5 === 4 ? 429 : 200 });
        done += 1;
      } catch {
        // A busy site can refuse a lease (reuse interval); the rest still counts.
      }
    }
    return done;
  }
}
