-- P1-017: four roles (BR-023). v1's warehouse went with the stock module
-- (BR-017).
--
-- Fails rather than remapping: a warehouse user has to be given a real role by
-- a person, and silently turning them into a viewer -- or worse, ops -- is a
-- permission change nobody approved.
DO $$
DECLARE n bigint;
BEGIN
    SELECT count(*) INTO n FROM users WHERE role = 'warehouse';
    IF n > 0 THEN
        RAISE EXCEPTION '% user(s) still hold the warehouse role; give each one owner, admin, ops or viewer first', n;
    END IF;
END $$;

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('owner','admin','ops','viewer'));
