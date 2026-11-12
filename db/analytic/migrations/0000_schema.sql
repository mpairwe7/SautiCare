-- analytic store: schema and baseline hardening (ADR-0006).
-- Runs as the store's owner role. Runtime roles get privileges only through
-- explicit GRANTs in later migrations; nothing may create objects in public.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA analytic;
REVOKE ALL ON SCHEMA analytic FROM PUBLIC;
