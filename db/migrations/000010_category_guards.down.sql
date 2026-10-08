CREATE OR REPLACE FUNCTION categories_set_path() RETURNS trigger AS $$
DECLARE
  parent_path ltree;
  label text;
BEGIN
  label := slugify_label(NEW.name);
  IF label = '' THEN label := 'cat'; END IF;
  IF NEW.parent_id IS NOT NULL THEN
    SELECT path INTO STRICT parent_path FROM categories
     WHERE id = NEW.parent_id AND kind = NEW.kind;
  END IF;
  NEW.path := CASE WHEN parent_path IS NULL
                   THEN label::ltree ELSE parent_path || label::ltree END;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
