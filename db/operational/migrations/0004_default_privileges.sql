-- Revoke PUBLIC's default EXECUTE on every function this store's owner
-- creates from now on (review finding I2).
--
-- PostgreSQL grants EXECUTE on new functions to PUBLIC. An earlier
-- "ALTER DEFAULT PRIVILEGES IN SCHEMA … REVOKE … FROM PUBLIC" is a no-op:
-- per-schema default privileges are additive and cannot remove the global
-- PUBLIC grant. Only a role-level (no IN SCHEMA) default can. Runs as the
-- store owner, so it applies to functions the owner creates in later
-- migrations; existing functions were already revoked explicitly.
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
