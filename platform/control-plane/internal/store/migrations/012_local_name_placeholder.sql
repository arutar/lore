-- Local accounts are created in Keycloak with the last name "-" (Keycloak requires one), and
-- sign-in stored the name claim with it ("Maria Lopez -"). Sign-in now drops it; this fixes the
-- names stored before. The audit log shows names from this table, so it follows.
UPDATE users
SET display_name = rtrim(left(display_name, length(display_name) - 2))
WHERE display_name LIKE '% -';
