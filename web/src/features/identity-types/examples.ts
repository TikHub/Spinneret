/** Identity type YAML examples from the design doc (section 5.1). */
export const IDENTITY_TYPE_EXAMPLES = {
  webCookie: `# Web crawler: cookie identity
name: web_cookie
client: web
description: Logged-in web cookies
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
`,
  appDevice: `# App crawler: device parameter identity
name: app_device
client: app
description: Registered app device parameters
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  cookies:    { type: cookie_map, sensitive: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    install_id: "{{ install_id }}"
  cookie_header: "{{ cookies }}"
  json: "{{ extra }}"
`,
} as const;

export type IdentityTypeExample = keyof typeof IDENTITY_TYPE_EXAMPLES;

/** Starting spec of a new identity type. */
export const EMPTY_SPEC = `name: my_identity_type
client: web
fields:
  token: { type: string, required: true, sensitive: true }
activation: probe
deliver:
  headers:
    Authorization: "Bearer {{ token }}"
`;

/** Default preview payload when no fields are known. */
export const DEFAULT_PREVIEW_PAYLOAD = '{\n  \n}';
