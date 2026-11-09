using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;

namespace LoreCompanion;

public partial class OperationWindow : Window
{
    private readonly OperationRequest request;
    private readonly ClientSettings settings = ClientSettings.Load();
    private bool running;

    public OperationWindow(OperationRequest request)
    {
        this.request = request;
        InitializeComponent();
        Title = $"Lore {request.Operation}";
        if (request.Operation == CompanionOperation.Clone)
        {
            Icon = new System.Windows.Media.Imaging.BitmapImage(new Uri("pack://application:,,,/LoreClone.ico"));
            HeaderIconText.Text = "\u2193";
        }
        TitleText.Text = request.Operation == CompanionOperation.Clone ? "Clone repository" : $"Lore {request.Operation}";
        Execute.Content = request.Operation.ToString();
        RepositoryUrl.Text = request.RepositoryUrl;
        TargetPath.Text = request.TargetPath;

        if (request.Operation == CompanionOperation.Clone && string.IsNullOrWhiteSpace(TargetPath.Text) && request.Paths.Length > 0)
        {
            var directory = System.IO.Directory.Exists(request.Paths[0]) ? request.Paths[0] : System.IO.Path.GetDirectoryName(request.Paths[0]);
            if (!string.IsNullOrEmpty(directory))
            {
                TargetPath.Text = System.IO.Path.Combine(directory, "repository");
            }
        }
        if (request.Operation != CompanionOperation.Clone)
        {
            RepositoryUrl.Text = string.Join(Environment.NewLine, request.Paths);
            RepositoryUrl.IsReadOnly = true;
            RepositoryUrl.Height = 76;
            RepositoryUrl.AcceptsReturn = true;
            if (request.Operation == CompanionOperation.Commit)
            {
                TargetPath.Text = request.Message;
            }
            else
            {
                TargetPath.IsReadOnly = true;
            }
        }
        ApplyTheme(settings.Theme);
        Loaded += async (_, _) =>
        {
            if (request.Operation != CompanionOperation.Clone)
            {
                await RunOperation();
            }
        };
    }

    private void Browse(object sender, RoutedEventArgs e)
    {
        var dialog = new Microsoft.Win32.OpenFolderDialog { InitialDirectory = TargetPath.Text };
        if (dialog.ShowDialog(this) == true)
        {
            TargetPath.Text = dialog.FolderName;
        }
    }

    private void Cancel(object sender, RoutedEventArgs e) => Close();

    private async void Run(object sender, RoutedEventArgs e) => await RunOperation();

    private async Task RunOperation()
    {
        if (running)
        {
            return;
        }
        request.RepositoryUrl = RepositoryUrl.Text.Trim();
        request.TargetPath = TargetPath.Text.Trim();
        if (request.Operation == CompanionOperation.Commit)
        {
            request.Message = TargetPath.Text;
        }
        try
        {
            request.Validate();
            running = true;
            Execute.IsEnabled = false;
            Progress.Visibility = Visibility.Visible;
            Output.Clear();
            Output.Foreground = (Brush)Resources["TextBrush"];
            await LoreOperations.ExecuteAsync(request, Report, CancellationToken.None);
            Report("Done.");
        }
        catch (Exception exception)
        {
            Progress.Visibility = Visibility.Collapsed;
            Output.Foreground = new SolidColorBrush(Color.FromRgb(242, 139, 139));
            Report(exception.Message);
        }
        finally
        {
            running = false;
            Execute.IsEnabled = true;
            Progress.Visibility = Visibility.Collapsed;
        }
    }

    private void Report(string text)
    {
        if (!Dispatcher.CheckAccess())
        {
            Dispatcher.BeginInvoke(() => Report(text));
            return;
        }
        Output.Visibility = Visibility.Visible;
        OutputRow.Height = new GridLength(1, GridUnitType.Star);
        if (request.Operation == CompanionOperation.Clone)
        {
            Height = 480;
        }
        Output.AppendText(text + Environment.NewLine);
    }

    private void ApplyTheme(string theme)
    {
        var light = theme == "light";
        SetBrush("WindowBrush", light ? "#F2EDD5" : "#0D1117");
        SetBrush("PanelBrush", light ? "#FFFDF6" : "#161B22");
        SetBrush("InputBrush", light ? "#FFFFFF" : "#21262D");
        SetBrush("TextBrush", light ? "#04261E" : "#E6EDF3");
        SetBrush("MutedBrush", light ? "#405C4F" : "#8B949E");
        SetBrush("AccentBrush", light ? "#F26716" : "#58A6FF");
        SetBrush("OnAccentBrush", light ? "#FFFDF6" : "#0D1117");
        SetBrush("BorderBrush", light ? "#C9C5B5" : "#30363D");
    }

    private void SetBrush(string key, string color) => Resources[key] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(color));

    protected override void OnClosing(System.ComponentModel.CancelEventArgs e)
    {
        e.Cancel = running;
        base.OnClosing(e);
    }
}
