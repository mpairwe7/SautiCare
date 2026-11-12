-- Chain hash at one sequence, for the checkpointer's anchor check (review
-- finding C1). The appender cannot read entries; it may only ask whether the
-- ledger still holds the last witnessed head at its sequence before it signs
-- a new checkpoint. Returns NULL when there is no such entry (truncation).
CREATE FUNCTION audit.hash_at(p_seq bigint) RETURNS bytea
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = audit, pg_temp
AS $$ SELECT e.hash FROM audit.entries e WHERE e.seq = p_seq $$;

REVOKE ALL ON FUNCTION audit.hash_at(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.hash_at(bigint) TO audit_appender;
