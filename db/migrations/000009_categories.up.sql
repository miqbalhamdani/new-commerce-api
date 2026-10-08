-- P1-022: categories with a trigger-maintained ltree path (03-erd.md §3.3,
-- §3.7; BR-031, BR-032). The cycle guard and same-name disambiguation are
-- P1-023, which replaces categories_set_path.

CREATE TABLE categories (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    parent_id   uuid,                   -- same tenant and same kind: FK below, trigger
    -- BR-031. Independent trees; one product may sit in several at once.
    kind        text NOT NULL DEFAULT 'category'
                CHECK (kind IN ('category','series','collection','activity','custom')),
    name        text NOT NULL,
    path        ltree NOT NULL,         -- derived by trigger, never from a client (BR-032)
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, kind, path)
);
CREATE INDEX ON categories USING gist (path);
CREATE INDEX ON categories (tenant_id, kind, parent_id);
ALTER TABLE categories ADD CONSTRAINT categories_id_tenant_uq UNIQUE (id, tenant_id);
ALTER TABLE categories ADD CONSTRAINT categories_same_tenant_as_parent       -- BR-004
    FOREIGN KEY (parent_id, tenant_id) REFERENCES categories (id, tenant_id);

SELECT enable_tenant_rls('categories');

-- ltree labels accept only [A-Za-z0-9_], so names are slugified.
CREATE FUNCTION slugify_label(txt text) RETURNS text AS $$
  SELECT regexp_replace(
           regexp_replace(lower(unaccent(coalesce(txt,''))), '[^a-z0-9]+', '_', 'g'),
           '^_+|_+$', '', 'g');
$$ LANGUAGE sql STABLE;

-- BEFORE: compute this row's own path from its parent's.
CREATE FUNCTION categories_set_path() RETURNS trigger AS $$
DECLARE
  parent_path ltree;
  label text;
BEGIN
  label := slugify_label(NEW.name);
  IF label = '' THEN label := 'cat'; END IF;

  IF NEW.parent_id IS NOT NULL THEN
    SELECT path INTO STRICT parent_path FROM categories
     WHERE id = NEW.parent_id AND kind = NEW.kind;   -- a parent is in the same tree (BR-031)
  END IF;

  NEW.path := CASE WHEN parent_path IS NULL
                   THEN label::ltree ELSE parent_path || label::ltree END;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER categories_path_biu
  BEFORE INSERT OR UPDATE OF name, parent_id ON categories
  FOR EACH ROW EXECUTE FUNCTION categories_set_path();

-- AFTER: rebase every descendant when this row's path changed, in the one
-- UPDATE statement (BR-032).
CREATE FUNCTION categories_move_subtree() RETURNS trigger AS $$
BEGIN
  -- The UPDATE below re-fires this trigger on each descendant. Descendants are
  -- already rebased by that single statement, so stop at depth 1.
  IF pg_trigger_depth() > 1 THEN RETURN NULL; END IF;

  IF NEW.path IS DISTINCT FROM OLD.path THEN
    UPDATE categories
       SET path = NEW.path || subpath(path, nlevel(OLD.path))
     WHERE tenant_id = NEW.tenant_id
       AND kind = NEW.kind              -- other kinds can share label paths
       AND path <@ OLD.path
       AND id <> NEW.id;
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;

-- OF name, parent_id, not OF path: a column list matches the UPDATE's SET
-- list, and a move sets parent_id while the BEFORE trigger changes path.
CREATE TRIGGER categories_move_aiu
  AFTER UPDATE OF name, parent_id ON categories
  FOR EACH ROW EXECUTE FUNCTION categories_move_subtree();
