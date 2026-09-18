import { Code, ConnectError, type Interceptor, type Transport } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';

import { getAuthBridge } from './authBridge';
import { REASON_HEADER } from './errors';

export const CSRF_HEADER = 'X-Spinneret-CSRF';
export const TENANT_HEADER = 'X-Spinneret-Tenant';

const AUTH_SERVICE = 'spinneret.v1.AuthService';
/** AuthService RPCs whose unauthenticated errors are expected and handled by the caller. */
const SILENT_UNAUTHENTICATED_METHODS = new Set(['Login', 'GetMe']);
/** A wrong current password must not end the session; only an invalid session does. */
const SESSION_INVALID_REASON = 'session_invalid';

function isSessionLoss(serviceName: string, methodName: string, err: ConnectError): boolean {
  if (err.code !== Code.Unauthenticated) return false;
  if (serviceName !== AUTH_SERVICE) return true;
  if (SILENT_UNAUTHENTICATED_METHODS.has(methodName)) return false;
  if (methodName === 'ChangePassword') return err.metadata.get(REASON_HEADER) === SESSION_INVALID_REASON;
  return true;
}

/** Adds the CSRF header and the active tenant to every request. */
export const headersInterceptor: Interceptor = (next) => async (req) => {
  req.header.set(CSRF_HEADER, '1');
  const tenantId = getAuthBridge().getTenantId();
  if (tenantId) {
    req.header.set(TENANT_HEADER, tenantId);
  }
  return next(req);
};

/** Reports unauthenticated errors (expired or revoked sessions) to the auth bridge. */
export const unauthenticatedInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (err) {
    const connectErr = ConnectError.from(err);
    if (isSessionLoss(req.service.typeName, req.method.name, connectErr)) {
      getAuthBridge().onUnauthenticated(connectErr);
    }
    throw err;
  }
};

/** Creates the Connect (JSON) transport used by all console clients. */
export function createTransport(baseUrl: string = window.location.origin): Transport {
  return createConnectTransport({
    baseUrl,
    useBinaryFormat: false,
    jsonOptions: { ignoreUnknownFields: true },
    interceptors: [unauthenticatedInterceptor, headersInterceptor],
  });
}

/** Shared transport instance. */
export const transport: Transport = createTransport();
