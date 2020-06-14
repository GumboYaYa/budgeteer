from django.http import HttpResponse, HttpResponseRedirect
from django.shortcuts import render

from .forms import FileUploadForm
from .utils import cnvt_date, cnvt_float, rm_spaces

# TODO: Strip fields if necessary
# TODO: Convert figure to decimal , > .
# TODO: Convert date


def upload_file(request):
    if request.method == "POST":
        form = FileUploadForm(request.POST, request.FILES)
        csv_file = request.FILES["csv_file"]

        file_data = csv_file.read().decode("utf-8")
        lines = file_data.splitlines()

        for line in lines:
            fields = line.split(";")
            data_dict = {}
            data_dict["iban"] = fields[0]
            data_dict["date_booking"] = cnvt_date(fields[1])
            data_dict["reference"] = fields[4]
            data_dict["optionee_name"] = rm_spaces(fields[11])
            data_dict["optionee_iban"] = fields[12]
            data_dict["optionee_bic"] = fields[13]
            data_dict["figure"] = cnvt_float(fields[14])
            data_dict["currency"] = fields[15]

            form = FileUploadForm(data_dict)

            if form.is_valid():
                form.save()
                return HttpResponseRedirect("/success/url/")
    else:
        form = FileUploadForm()
    return render(request, "upload/index.html", {"form": form})

    # return HttpResponse('This is the import page.')

    # data = {}
    # if "GET" == request.method:
    # 	return render(request, "myapp/upload_csv.html", data)
    # # if not GET, then proceed
    # try:
    # 	csv_file = request.FILES["csv_file"]
    # 	if not csv_file.name.endswith('.csv'):
    # 		messages.error(request,'File is not CSV type')
    # 		return HttpResponseRedirect(reverse("myapp:upload_csv"))
    #     #if file is too large, return
    # 	if csv_file.multiple_chunks():
    # 		messages.error(request,"Uploaded file is too big (%.2f MB)." % (csv_file.size/(1000*1000),))
    # 		return HttpResponseRedirect(reverse("myapp:upload_csv"))

    # 	file_data = csv_file.read().decode("utf-8")

    # 	lines = file_data.split("\n")
    # 	#loop over the lines and save them in db. If error , store as string and then display
    # 	for line in lines:
    # 		fields = line.split(",")
    # 		data_dict = {}
    # 		data_dict["name"] = fields[0]
    # 		data_dict["start_date_time"] = fields[1]
    # 		data_dict["end_date_time"] = fields[2]
    # 		data_dict["notes"] = fields[3]
    # 		try:
    # 			form = EventsForm(data_dict)
    # 			if form.is_valid():
    # 				form.save()
    # 			else:
    # 				logging.getLogger("error_logger").error(form.errors.as_json())
    # 		except Exception as e:
    # 			logging.getLogger("error_logger").error(repr(e))
    # 			pass

    # except Exception as e:
    # 	logging.getLogger("error_logger").error("Unable to upload file. "+repr(e))
    # 	messages.error(request,"Unable to upload file. "+repr(e))

    # return HttpResponseRedirect(reverse("myapp:upload_csv"))
