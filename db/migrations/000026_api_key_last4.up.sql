-- api_keys.key_last4 (contracts v2.12.0, BR-028): the key's last four
-- characters, so a list can show sf_live_••••42ab. NULL for keys made before.

ALTER TABLE api_keys ADD COLUMN key_last4 text;
