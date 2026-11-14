-- English is the default interface language for new installations. Existing installations keep
-- the language already stored in their settings.
ALTER TABLE settings ALTER COLUMN language SET DEFAULT 'en';
