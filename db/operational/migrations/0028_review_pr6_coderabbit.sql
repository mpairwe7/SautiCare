-- CodeRabbit review of PR #6 (head 8d3179d), finding 1, for the gazetteer
-- tables 0014 added. A published gazetteer version takes no additions:
-- 0007 refuses an INSERT under a version already in op.config_versions
-- (op.gazetteer_unpublished) for wards, zones, landmarks and aliases, and
-- stages and facilities are versioned with them. The loader inserts a
-- version's rows before publishing it, in one transaction.
CREATE TRIGGER stages_unpublished BEFORE INSERT ON op.stages
    FOR EACH ROW EXECUTE FUNCTION op.gazetteer_unpublished();
CREATE TRIGGER facilities_unpublished BEFORE INSERT ON op.facilities
    FOR EACH ROW EXECUTE FUNCTION op.gazetteer_unpublished();
