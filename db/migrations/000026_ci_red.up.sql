-- [throwaway] fine on an empty database, impossible once a shop exists
ALTER TABLE tenants ADD COLUMN ci_red text NOT NULL;
