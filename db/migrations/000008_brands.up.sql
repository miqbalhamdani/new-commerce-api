-- P1-020: brands (03-erd.md §3.3, BR-030).

-- URL slug for brands and products: lower-case a-z0-9 joined by hyphens,
-- accents folded ("Café Ñ" -> "cafe-n"). One definition, so a brand created by
-- the API and one created by an import slugify identically. unaccent is STABLE
-- (it reads a dictionary), so this is too.
CREATE FUNCTION slugify(txt text) RETURNS text
LANGUAGE sql STABLE AS $$
  SELECT trim(both '-' from
           regexp_replace(lower(unaccent(coalesce(txt, ''))), '[^a-z0-9]+', '-', 'g'));
$$;

CREATE TABLE brands (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    name        text NOT NULL,
    slug        text NOT NULL,          -- derived from name, never from a client (BR-008, BR-030)
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug)            -- archived brands included (BR-030)
);
CREATE INDEX ON brands (tenant_id) WHERE archived_at IS NULL;
CREATE INDEX ON brands (tenant_id, lower(name));   -- import matches brands by name (BR-103)
ALTER TABLE brands ADD CONSTRAINT brands_id_tenant_uq UNIQUE (id, tenant_id);

SELECT enable_tenant_rls('brands');
