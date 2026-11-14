-- P1-922: plans (03-erd.md §3.2; BR-160). Every shop that exists now keeps
-- everything it had, so the default is jualan; signup and the platform
-- console name the plan themselves.
ALTER TABLE tenants
    ADD COLUMN plan       text NOT NULL DEFAULT 'jualan' CHECK (plan IN ('mulai','katalog','jualan')),
    ADD COLUMN plan_until date;
