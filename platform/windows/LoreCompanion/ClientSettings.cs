using System.Text.Json;

namespace LoreCompanion;

internal sealed class ClientSettings
{
    public string Theme { get; set; } = "midnight";

    public static ClientSettings Load()
    {
        try
        {
            var settings = JsonSerializer.Deserialize<ClientSettings>(File.ReadAllText(Path())) ?? new ClientSettings();
            if (settings.Theme is not ("midnight" or "light"))
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
