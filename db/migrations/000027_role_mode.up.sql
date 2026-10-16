-- db_role_mode() for databases bootstrapped before 000001 created it.
--
-- Such a database was created with app_user and auth_lookup, so it is in the
-- default mode by definition (BR-001). A database bootstrapped since already
-- has the function, frozen to whichever mode it was created in; leave it be.

DO $$
BEGIN
    IF to_regprocedure('db_role_mode()') IS NULL THEN
        CREATE FUNCTION db_role_mode() RETURNS text
        LANGUAGE sql IMMUTABLE
        AS $body$ SELECT 'strong'::text $body$;

        COMMENT ON FUNCTION db_role_mode() IS
            'strong or limited, fixed when the schema was created (BR-001). See 03-erd.md section 3.1.';
    END IF;
END
$$;
