using System.Text.Json;

namespace LoreCompanion;

internal sealed class ClientSettings
{
    /// <summary>The themes of the WebUI (platform/web/src/themes.css); the companion windows use the same set.</summary>
    public static readonly string[] Themes = ["midnight", "sky", "ember", "sage", "aurora", "ocean", "retro", "autumn", "light", "cyberpunk"];

    public string Theme { get; set; } = "midnight";
    public Dictionary<string, string> SelectedAccounts { get; set; } = new(StringComparer.OrdinalIgnoreCase);

    public string SelectedAccount(string server) => SelectedAccounts.TryGetValue(server, out var identity) ? identity : "";

    public void SelectAccount(string server, string identity)
    {
        SelectedAccounts[server] = identity;
        Save();
    }

    public void SetTheme(string theme)
    {
        if (!Themes.Contains(theme)) throw new InvalidDataException($"Unknown theme: {theme}");
        Theme = theme;
        Save();
    }

    public void RemoveAccount(string identity)
    {
        foreach (var server in SelectedAccounts.Where(pair => pair.Value == identity).Select(pair => pair.Key).ToArray())
        {
            SelectedAccounts.Remove(server);
        }
        Save();
    }

    public static ClientSettings Load()
    {
        try
        {
            var settings = JsonSerializer.Deserialize<ClientSettings>(File.ReadAllText(Path())) ?? new ClientSettings();
            if (!Themes.Contains(settings.Theme))
            {
                settings.Theme = "midnight";
            }
            return settings;
        }
        catch
        {
            return new ClientSettings();
        }
    }

    public void Save()
    {
        var path = Path();
        Directory.CreateDirectory(System.IO.Path.GetDirectoryName(path)!);
        File.WriteAllText(path, JsonSerializer.Serialize(this, new JsonSerializerOptions { WriteIndented = true }));
    }

    private static string Path() => System.IO.Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Lore", "client.json");
}
