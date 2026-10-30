// Customer-portal login URL for the admin dashboard's "copy portal link" buttons.
//
// A workspace created with a dedicated database needs ?w=<workspace id> on the
// link, or the API cannot find the customer (the portal login has no session to
// take the workspace from). Every other workspace gets the link it always did.
// `account` is the GET /v1/account payload, which carries `id` and `database_name`.
export function portalLoginUrl(email, account) {
  const base = `${window.location.origin}/portal/login?email=${encodeURIComponent(email || "")}`;
  return account?.database_name && account?.id
    ? `${base}&w=${encodeURIComponent(account.id)}`
    : base;
}
