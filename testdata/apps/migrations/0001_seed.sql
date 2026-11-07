-- Fixture of apps_test.go: an app data migration embedded with //go:embed.
CREATE TABLE fixture_log (entry text NOT NULL);
INSERT INTO fixture_log (entry) VALUES ('seeded');
