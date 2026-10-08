-- P1-023: categories_set_path gains the cycle guard (BR-034) and same-name
-- sibling disambiguation (BR-035), completing 03-erd.md §3.7.
CREATE OR REPLACE FUNCTION categories_set_path() RETURNS trigger AS $$
DECLARE
  parent_path ltree;
  base text; label text; candidate ltree; n int := 0;
BEGIN
  base := slugify_label(NEW.name);
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
  -- categories keep their path, so they count.
  label := base;
  LOOP
    candidate := CASE WHEN parent_path IS NULL
                      THEN label::ltree ELSE parent_path || label::ltree END;
    EXIT WHEN NOT EXISTS (
      SELECT 1 FROM categories
       WHERE tenant_id = NEW.tenant_id AND kind = NEW.kind
         AND path = candidate AND id IS DISTINCT FROM NEW.id);
    n := n + 1;
    label := base || '_' || n;
  END LOOP;

  NEW.path := candidate;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
