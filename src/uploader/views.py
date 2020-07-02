from django.http import HttpResponse, HttpResponseRedirect
from django.shortcuts import render
from django.views.generic import ListView

from .forms import FileUploadForm
from .utils import cnvt_date, cnvt_float, rm_spaces, rm_quotes
from .models import Transaction

# TODO: Implement with ModelForm - is it useful in this case?
# TODO: How to identify each transaction unambiguously?
# TODO: Add dropdown for each bank (different column mapping)

def upload_file(request):
    if request.method == "POST":
        form = FileUploadForm(request.POST, request.FILES)
        csv_file = request.FILES["file"]

        def cleanutf(str):
            return ''.join([c if len(c.encode('utf-8')) < 3 else '?' for c in str])

        file_data = csv_file.read().decode("utf-8", errors='replace')
        lines = file_data.splitlines()

        for line in lines[1:]:
            # clean_line = cleanutf(line)
            fields = line.split(";")
            fields = [x.strip("\"") for x in fields]
            data_dict = {}
            data_dict["iban"] = fields[0]
            data_dict["date_booking"] = cnvt_date(fields[1])
            data_dict["reference"] = fields[4]
            data_dict["optionee_name"] = rm_spaces(fields[11])
            data_dict["optionee_iban"] = fields[12]
            data_dict["optionee_bic"] = fields[13]
            data_dict["figure"] = cnvt_float(fields[14])
            data_dict["currency"] = fields[15]

            # print(data_dict)

            form = FileUploadForm(data_dict)

            if form.is_valid():
                form.save()

        return HttpResponseRedirect("success/")
    else:
        form = FileUploadForm()
        print("Form is not POST method!")
    return render(request, "uploader/index.html", {"form": form})


class ImportedData(ListView):
    model = Transaction
    template_name = "uploader/success.html"
    context_object_name = "imported_data"

    # return HttpResponse('This is the success page.')
