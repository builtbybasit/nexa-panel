CREATE TRIGGER backup_copies_require_plan
BEFORE INSERT ON backup_copies
FOR EACH ROW WHEN NOT EXISTS (SELECT 1 FROM backup_plans WHERE id = NEW.plan_id)
BEGIN
	SELECT RAISE(ABORT, 'backup copy requires an existing plan');
END;
--bun:split
CREATE TRIGGER backup_plans_require_no_copies
BEFORE DELETE ON backup_plans
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM backup_copies WHERE plan_id = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'backup plan still has stored copies');
END;
