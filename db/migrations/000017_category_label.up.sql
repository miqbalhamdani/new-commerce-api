-- Contracts v2.7.0: a category's own path segment can be set by the client
-- (BR-032). NULL derives it from the name as before; a set label that a
-- sibling already holds is refused, not suffixed.
ALTER TABLE categories ADD label text CHECK (label ~ '^[a-z0-9]+(_[a-z0-9]+)*$');

CREATE OR REPLACE FUNCTION categories_set_path() RETURNS trigger AS $$
DECLARE
  parent_path ltree;
  base text; lbl text; candidate ltree; n int := 0;
BEGIN
  base := coalesce(NEW.label, slugify_label(NEW.name));
  IF base = '' THEN base := 'cat'; END IF;

  IF NEW.parent_id IS NOT NULL THEN
    SELECT path INTO STRICT parent_path FROM categories
     WHERE id = NEW.parent_id AND kind = NEW.kind;   -- a parent is in the same tree (BR-031)
    -- A category cannot be moved beneath itself or its own descendant.
    IF TG_OP = 'UPDATE' AND parent_path <@ OLD.path THEN
      RAISE EXCEPTION 'cannot move category % beneath its own descendant', NEW.id
        USING ERRCODE = 'P0001', HINT = 'category_cycle';
    END IF;
  END IF;

  -- Two siblings named "Jackets" slugify identically; disambiguate. Archived
  -- categories keep their path, so they count. A label the client chose is
  -- refused instead of silently changed.
  lbl := base;
  LOOP
    candidate := CASE WHEN parent_path IS NULL
                      THEN lbl::ltree ELSE parent_path || lbl::ltree END;
    EXIT WHEN NOT EXISTS (
      SELECT 1 FROM categories
       WHERE tenant_id = NEW.tenant_id AND kind = NEW.kind
         AND path = candidate AND id IS DISTINCT FROM NEW.id);
    IF NEW.label IS NOT NULL THEN
      RAISE EXCEPTION 'category label % is taken', NEW.label
        USING ERRCODE = 'P0001', HINT = 'category_label_taken';
    END IF;
    n := n + 1;
    lbl := base || '_' || n;
  END LOOP;

  NEW.path := candidate;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

-- A relabel moves the subtree like a rename does.
DROP TRIGGER categories_path_biu ON categories;
CREATE TRIGGER categories_path_biu
  BEFORE INSERT OR UPDATE OF name, parent_id, label ON categories
  FOR EACH ROW EXECUTE FUNCTION categories_set_path();

DROP TRIGGER categories_move_aiu ON categories;
CREATE TRIGGER categories_move_aiu
  AFTER UPDATE OF name, parent_id, label ON categories
  FOR EACH ROW EXECUTE FUNCTION categories_move_subtree();
