using System.Text;
using System.Windows;

namespace LoreCompanion;

internal static class Program
{
    public const int MaximumRequestBytes = 1_048_576;

    [STAThread]
    private static void Main(string[] args)
    {
        if (args.Length != 2 || args[0] != "--request")
        {
            MessageBox.Show("Use Lore Explorer integration to start an operation.", "Lore Companion", MessageBoxButton.OK, MessageBoxImage.Information);
            return;
        }
        try
        {
            var path = args[1];
            var info = new FileInfo(path);
            if (info.Length <= 0 || info.Length > MaximumRequestBytes)
            {
                throw new InvalidDataException("Invalid request size");
            }
            var json = File.ReadAllText(path, Encoding.UTF8);
            File.Delete(path);
            var request = OperationRequest.Parse(json);
            if (!WebViewOperationWindow.IsRuntimeAvailable())
            {
                throw new InvalidOperationException("WebView2 Runtime is required. Run the Lore installer to install it, then try again.");
            }
            new Application().Run(new WebViewOperationWindow(request));
        }
        catch (Exception exception)
        {
            MessageBox.Show(exception.Message, "Lore Companion", MessageBoxButton.OK, MessageBoxImage.Error);
        }
    }
}
