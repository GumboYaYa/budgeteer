from django.http import HttpResponse, HttpResponseRedirect
from django.shortcuts import render
from django.views.generic import ListView

from .forms import FileUploadForm
from .models import Transaction
from .utils import cnvt_csv
from .profiles import run_template


# TODO: Implement with ModelForm - is it useful in this case?
# TODO: How to identify each transaction unambiguously?
# TODO: Special case: Kreditkartenabrechnung > No IBAN


def upload_file(request):

    if request.method == "POST":
        form = FileUploadForm(request.POST, request.FILES)
        csv_file = request.FILES["file"]
        file_data = csv_file.read().decode("utf-8", errors="replace")
        fields = cnvt_csv(file_data)

        bank_id = request.POST["bank"]
        run_template(bank_id, fields)

        return HttpResponseRedirect("success/")
    else:
        form = FileUploadForm()
        print("Form is not POST method!")
    return render(request, "uploader/index.html", {"form": form})


class ImportedData(ListView):
    model = Transaction
    template_name = "uploader/success.html"
    context_object_name = "imported_data"
