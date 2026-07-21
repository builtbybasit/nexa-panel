CREATE TRIGGER domains_guard_site_primary
BEFORE INSERT ON sites
WHEN EXISTS (SELECT 1 FROM domains WHERE hostname = NEW.primary_domain)
BEGIN SELECT RAISE(ABORT, 'hostname is already managed'); END;
--bun:split
CREATE TRIGGER sites_guard_domain_hostname
BEFORE INSERT ON domains
WHEN EXISTS (SELECT 1 FROM sites WHERE primary_domain = NEW.hostname AND id <> NEW.site_id)
BEGIN SELECT RAISE(ABORT, 'hostname is already a site primary domain'); END;
